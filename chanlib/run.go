package chanlib

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/obs"
)

// RunOpts configures Run. Start is called after registration; its handler serves
// on ListenAddr, its cleanup func runs before the HTTP server closes.
type RunOpts struct {
	Name          string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	Prefixes      []string
	Caps          map[string]bool
	Start         func(ctx context.Context, rc *RouterClient) (http.Handler, func(), error)
}

// Run is the shared main loop for channel adapter daemons.
func Run(opts RunOpts) {
	defer obs.Setup(opts.Name, os.Getenv("ARIZUKO_INSTANCE"))()
	if opts.ChannelSecret == "" {
		slog.Error("CHANNEL_SECRET not set; refusing to start unauthenticated adapter")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	rc := NewRouterClient(opts.RouterURL, opts.ChannelSecret)
	// Split (spec 5/1): exchange AUTHD_SERVICE_KEY for a service:<adapter> JWT
	// and present it on routd's JWT-gated calls (/v1/messages, /v1/pane).
	// Registration still uses CHANNEL_SECRET regardless.
	//
	// The exchange principal is the DAEMON name (AUTHD_SERVICE_NAME, set by compose
	// to the base daemon — e.g. teled), NOT opts.Name (the CHANNEL_NAME, e.g.
	// telegram / telegram-rhias). authd seeds + grants service:<daemon>, and
	// multi-account variants share the base principal (spec 5/R). Falling back to
	// opts.Name only when AUTHD_SERVICE_NAME is unset would 401 whenever the
	// channel name differs from the daemon (cost a krons telegram outage 2026-06-07).
	authdURL, svcKey := os.Getenv("AUTHD_URL"), os.Getenv("AUTHD_SERVICE_KEY")
	svcName := EnvOr("AUTHD_SERVICE_NAME", opts.Name)
	if authdURL != "" && svcKey != "" {
		src, err := auth.ServiceToken(authdURL, svcName, svcKey)
		if err != nil {
			slog.Error("service-token source", "daemon", svcName, "err", err)
			os.Exit(1)
		}
		rc.SetServiceToken(src.Token)
		slog.Info("service-token auth enabled", "daemon", svcName, "authd", authdURL)
	}
	if _, err := rc.Register(opts.Name, opts.ListenURL, opts.Prefixes, opts.Caps); err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("registered with router", "url", opts.RouterURL)

	handler, stop, err := opts.Start(ctx, rc)
	if err != nil {
		slog.Error("adapter start failed", "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", opts.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", opts.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", opts.ListenAddr)
	srv := &http.Server{Handler: handler}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		slog.Error("http server failed", "err", err)
		os.Exit(1)
	}
	slog.Info("shutting down")
	rc.Deregister()
	if stop != nil {
		stop()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("graceful shutdown failed", "err", err)
	}
}

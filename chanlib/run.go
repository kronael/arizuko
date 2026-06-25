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
	Name       string
	RouterURL  string
	ListenAddr string
	ListenURL  string
	Prefixes   []string
	Caps       map[string]bool
	Start      func(ctx context.Context, rc *RouterClient) (http.Handler, func(), error)
}

// Run is the shared main loop for channel adapter daemons.
func Run(opts RunOpts) {
	defer obs.Setup(opts.Name, os.Getenv("ARIZUKO_INSTANCE"))()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	rc := NewRouterClient(opts.RouterURL)
	// Split (spec 5/1) is the only topology: exchange AUTHD_SERVICE_KEY for a
	// service:<adapter> JWT and present it on EVERY routd call — registration,
	// /v1/messages, /v1/pane (HMAC retire step 5; no CHANNEL_SECRET remains).
	//
	// AUTHD_URL + AUTHD_SERVICE_KEY are required — compose always emits them, and
	// local-dev brings up authd through the same tooling. No fallback: routd's
	// verifier rejects an unsigned call, so a missing key must fail loudly, not
	// silently 401 every outbound (cost a krons telegram outage 2026-06-07).
	//
	// The exchange principal is the DAEMON name (AUTHD_SERVICE_NAME, set by compose
	// to the base daemon — e.g. teled), NOT opts.Name (the CHANNEL_NAME, e.g.
	// telegram / telegram-rhias). authd seeds + grants service:<daemon>;
	// multi-account variants share the base principal (spec 5/R).
	authdURL, svcKey := os.Getenv("AUTHD_URL"), os.Getenv("AUTHD_SERVICE_KEY")
	if authdURL == "" || svcKey == "" {
		slog.Error("AUTHD_URL + AUTHD_SERVICE_KEY required", "daemon", opts.Name)
		os.Exit(1)
	}
	svcName := EnvOr("AUTHD_SERVICE_NAME", opts.Name)
	src, err := auth.ServiceToken(authdURL, svcName, svcKey)
	if err != nil {
		slog.Error("service-token source", "daemon", svcName, "err", err)
		os.Exit(1)
	}
	rc.SetServiceToken(src.Token)
	slog.Info("service-token auth enabled", "daemon", svcName, "authd", authdURL)
	if _, err := rc.Register(opts.Name, opts.ListenURL, opts.Prefixes, opts.Caps); err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("registered with router", "url", opts.RouterURL)

	// Re-register every 3 minutes: routd auto-deregisters after 3 consecutive
	// health failures (e.g. transient Telegram/Slack API 502). Under split
	// topology, JWT auth and channel registration are decoupled, so inbound
	// delivery still works (JWT passes) but outbound silently fails (channel
	// not in registry). The heartbeat keeps the entry alive without restart.
	go func() {
		t := time.NewTicker(3 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := rc.Register(opts.Name, opts.ListenURL, opts.Prefixes, opts.Caps); err != nil {
					slog.Warn("channel heartbeat failed", "name", opts.Name, "err", err)
				}
			}
		}
	}()

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

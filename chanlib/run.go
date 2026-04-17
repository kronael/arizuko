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
)

// RunOpts bundles per-adapter values for Run. Start is called after
// router registration succeeds; the returned handler serves on
// ListenAddr, and the cleanup func runs on shutdown before the HTTP
// server closes.
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

// Run is the shared main loop for channel adapter daemons: JSON logging,
// sigterm context, router registration, HTTP serve, graceful shutdown.
func Run(opts RunOpts) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	if opts.ChannelSecret == "" {
		slog.Error("CHANNEL_SECRET not set; refusing to start unauthenticated adapter")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	rc := NewRouterClient(opts.RouterURL, opts.ChannelSecret)
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

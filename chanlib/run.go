package chanlib

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

// RunOpts bundles the per-adapter values chanlib.Run consumes. Start is
// called after router registration succeeds; the returned handler is
// mounted on the listen address, and Stop runs on shutdown before the
// HTTP server closes.
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
// Adapters call it from main and supply Start to construct the handler
// and bot once the router accepts the registration.
func Run(opts RunOpts) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	if opts.ChannelSecret == "" {
		slog.Warn("CHANNEL_SECRET not set; HTTP endpoints unauthenticated")
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
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.Deregister()
	if stop != nil {
		stop()
	}
	srv.Close()
}

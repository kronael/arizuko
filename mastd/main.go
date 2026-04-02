package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/onvos/arizuko/chanlib"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := loadConfig()
	if cfg.ChannelSecret == "" {
		slog.Warn("CHANNEL_SECRET not set; HTTP endpoints unauthenticated")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	mc, err := newMastoClient(cfg)
	if err != nil {
		slog.Error("mastodon connect failed", "err", err)
		os.Exit(1)
	}

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	_, err = rc.Register(cfg.Name, cfg.ListenURL, []string{"mastodon:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("registered with router", "url", cfg.RouterURL)

	go mc.stream(ctx, rc)

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", cfg.ListenAddr)
	srv := &http.Server{Handler: newServer(cfg, mc).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.Deregister()
	srv.Close()
}

type config struct {
	Name          string
	InstanceURL   string
	AccessToken   string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "mastodon"),
		InstanceURL:   chanlib.MustEnv("MASTODON_INSTANCE_URL"),
		AccessToken:   chanlib.MustEnv("MASTODON_ACCESS_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9004"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://mastd:9004"),
	}
}

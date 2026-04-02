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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()
	if cfg.ChannelSecret == "" {
		slog.Warn("CHANNEL_SECRET not set; HTTP endpoints unauthenticated")
	}
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	bc, err := newBskyClient(cfg)
	if err != nil {
		slog.Error("bluesky auth failed", "err", err)
		os.Exit(1)
	}

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	_, err = rc.Register(cfg.Name, cfg.ListenURL,
		[]string{"bluesky:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("registered with router", "url", cfg.RouterURL)

	go bc.poll(ctx, rc)

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", cfg.ListenAddr)
	srv := &http.Server{Handler: newServer(cfg, bc).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.Deregister()
	srv.Close()
}

type config struct {
	Name          string
	Identifier    string
	Password      string
	Service       string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	DataDir       string
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "bluesky"),
		Identifier:    chanlib.MustEnv("BLUESKY_IDENTIFIER"),
		Password:      chanlib.MustEnv("BLUESKY_PASSWORD"),
		Service:       chanlib.EnvOr("BLUESKY_SERVICE", "https://bsky.social"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9005"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://bluesky:9005"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/bskyd"),
	}
}

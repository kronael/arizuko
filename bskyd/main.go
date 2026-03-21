package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	bc, err := newBskyClient(cfg)
	if err != nil {
		slog.Error("bluesky auth failed", "err", err)
		os.Exit(1)
	}

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.register(cfg.Name, cfg.ListenURL,
		[]string{"bluesky:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.token = token
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
	rc.deregister()
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
		Name:          envOr("CHANNEL_NAME", "bluesky"),
		Identifier:    mustEnv("BLUESKY_IDENTIFIER"),
		Password:      mustEnv("BLUESKY_PASSWORD"),
		Service:       envOr("BLUESKY_SERVICE", "https://bsky.social"),
		RouterURL:     mustEnv("ROUTER_URL"),
		ChannelSecret: envOr("CHANNEL_SECRET", ""),
		ListenAddr:    envOr("LISTEN_ADDR", ":9005"),
		ListenURL:     envOr("LISTEN_URL", "http://bluesky:9005"),
		DataDir:       envOr("DATA_DIR", "/srv/data/bskyd"),
	}
}

func envOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

func mustEnv(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	slog.Error("required env var missing", "key", k)
	os.Exit(1)
	return ""
}

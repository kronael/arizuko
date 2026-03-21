package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

	rc2, err := newRedditClient(cfg)
	if err != nil {
		slog.Error("reddit auth failed", "err", err)
		os.Exit(1)
	}

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.register(cfg.Name, cfg.ListenURL,
		[]string{"reddit:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.token = token
	slog.Info("registered with router", "url", cfg.RouterURL)

	go rc2.poll(ctx, rc)

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", cfg.ListenAddr)
	srv := &http.Server{Handler: newServer(cfg, rc2).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.deregister()
	srv.Close()
}

type config struct {
	Name          string
	ClientID      string
	ClientSecret  string
	Username      string
	Password      string
	Subreddits    []string
	UserAgent     string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
}

func loadConfig() config {
	srs := envOr("REDDIT_SUBREDDITS", "")
	var subreddits []string
	for _, s := range strings.Split(srs, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			subreddits = append(subreddits, s)
		}
	}
	return config{
		Name:          envOr("CHANNEL_NAME", "reddit"),
		ClientID:      mustEnv("REDDIT_CLIENT_ID"),
		ClientSecret:  mustEnv("REDDIT_CLIENT_SECRET"),
		Username:      mustEnv("REDDIT_USERNAME"),
		Password:      mustEnv("REDDIT_PASSWORD"),
		Subreddits:    subreddits,
		UserAgent:     envOr("REDDIT_USER_AGENT", "arizuko/1.0"),
		RouterURL:     mustEnv("ROUTER_URL"),
		ChannelSecret: envOr("CHANNEL_SECRET", ""),
		ListenAddr:    envOr("LISTEN_ADDR", ":9006"),
		ListenURL:     envOr("LISTEN_URL", "http://reditd:9006"),
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

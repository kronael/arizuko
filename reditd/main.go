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

	rc2, err := newRedditClient(cfg)
	if err != nil {
		slog.Error("reddit auth failed", "err", err)
		os.Exit(1)
	}
	rc2.loadCursors()

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.Register(cfg.Name, cfg.ListenURL,
		[]string{"reddit:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.Token = token
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
	rc.Deregister()
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
	DataDir       string
}

func loadConfig() config {
	srs := chanlib.EnvOr("REDDIT_SUBREDDITS", "")
	var subreddits []string
	for _, s := range strings.Split(srs, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			subreddits = append(subreddits, s)
		}
	}
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "reddit"),
		ClientID:      chanlib.MustEnv("REDDIT_CLIENT_ID"),
		ClientSecret:  chanlib.MustEnv("REDDIT_CLIENT_SECRET"),
		Username:      chanlib.MustEnv("REDDIT_USERNAME"),
		Password:      chanlib.MustEnv("REDDIT_PASSWORD"),
		Subreddits:    subreddits,
		UserAgent:     chanlib.EnvOr("REDDIT_USER_AGENT", "arizuko/1.0"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9006"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://reditd:9006"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/reditd"),
	}
}

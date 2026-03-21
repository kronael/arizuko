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

	db, err := openDB(cfg.DataDir)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	rc := newRouterClient(cfg.RouterURL, cfg.ChannelSecret)
	token, err := rc.register(cfg.Name, cfg.ListenURL,
		[]string{"email:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.token = token
	slog.Info("registered with router", "url", cfg.RouterURL)

	poller := newPoller(cfg, db)
	go poller.run(ctx, rc)

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.ListenAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("http server starting", "addr", cfg.ListenAddr)
	srv := &http.Server{Handler: newServer(cfg, db).handler()}
	go srv.Serve(ln)

	<-ctx.Done()
	slog.Info("shutting down")
	rc.deregister()
	srv.Close()
}

type config struct {
	Name          string
	IMAPHost      string
	SMTPHost      string
	Account       string
	Password      string
	IMAPPort      string
	SMTPPort      string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	DataDir       string
}

func loadConfig() config {
	return config{
		Name:          envOr("CHANNEL_NAME", "email"),
		IMAPHost:      mustEnv("EMAIL_IMAP_HOST"),
		SMTPHost:      mustEnv("EMAIL_SMTP_HOST"),
		Account:       mustEnv("EMAIL_ACCOUNT"),
		Password:      mustEnv("EMAIL_PASSWORD"),
		IMAPPort:      envOr("EMAIL_IMAP_PORT", "993"),
		SMTPPort:      envOr("EMAIL_SMTP_PORT", "587"),
		RouterURL:     mustEnv("ROUTER_URL"),
		ChannelSecret: envOr("CHANNEL_SECRET", ""),
		ListenAddr:    envOr("LISTEN_ADDR", ":9003"),
		ListenURL:     envOr("LISTEN_URL", "http://email:9003"),
		DataDir:       envOr("DATA_DIR", "/srv/data/emaid"),
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

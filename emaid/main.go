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
	token, err := rc.Register(cfg.Name, cfg.ListenURL,
		[]string{"email:"}, map[string]bool{"send_text": true})
	if err != nil {
		slog.Error("router registration failed", "err", err)
		os.Exit(1)
	}
	rc.Token = token
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
	rc.Deregister()
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
		Name:          chanlib.EnvOr("CHANNEL_NAME", "email"),
		IMAPHost:      chanlib.MustEnv("EMAIL_IMAP_HOST"),
		SMTPHost:      chanlib.MustEnv("EMAIL_SMTP_HOST"),
		Account:       chanlib.MustEnv("EMAIL_ACCOUNT"),
		Password:      chanlib.MustEnv("EMAIL_PASSWORD"),
		IMAPPort:      chanlib.EnvOr("EMAIL_IMAP_PORT", "993"),
		SMTPPort:      chanlib.EnvOr("EMAIL_SMTP_PORT", "587"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9003"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://email:9003"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/emaid"),
	}
}

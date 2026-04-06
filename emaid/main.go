package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:          cfg.Name,
		RouterURL:     cfg.RouterURL,
		ChannelSecret: cfg.ChannelSecret,
		ListenAddr:    cfg.ListenAddr,
		ListenURL:     cfg.ListenURL,
		Prefixes:      []string{"email:"},
		Caps:          map[string]bool{"send_text": true},
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			db, err := openDB(cfg.DataDir)
			if err != nil {
				slog.Error("db open failed", "err", err)
				return nil, nil, err
			}
			files := newAttachCache(1000)
			go newPoller(cfg, db, files).run(ctx, rc)
			return newServer(cfg, db, files).handler(), func() { db.Close() }, nil
		},
	})
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

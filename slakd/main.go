package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:          cfg.Name,
		RouterURL:     cfg.RouterURL,
		ChannelSecret: cfg.ChannelSecret,
		ListenAddr:    cfg.ListenAddr,
		ListenURL:     cfg.ListenURL,
		Prefixes:      []string{"slack:"},
		Caps: map[string]bool{
			"send_text": true, "send_file": true, "fetch_history": false,
			"edit": true, "like": true, "delete": true, "dislike": true, "post": true,
		},
		Start: func(_ context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("slack init failed", "err", err)
				return nil, nil, err
			}
			srv := newServer(cfg, b, b.isConnected, b.LastInboundAt)
			b.files = srv.files
			if err := b.start(rc); err != nil {
				slog.Error("slack auth.test failed", "err", err)
				return nil, nil, err
			}
			return srv.handler(), b.stop, nil
		},
	})
}

type config struct {
	Name          string
	BotToken      string
	SigningSecret string
	RouterURL     string
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	AssistantName string
	MediaMaxBytes int64
	CacheTTL      time.Duration
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "slack"),
		BotToken:      chanlib.MustEnv("SLACK_BOT_TOKEN"),
		SigningSecret: chanlib.MustEnv("SLACK_SIGNING_SECRET"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("SLAKD_CHANNEL_SECRET", chanlib.EnvOr("CHANNEL_SECRET", "")),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://slakd:8080"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		CacheTTL:      time.Duration(chanlib.EnvInt("SLAKD_USERS_CACHE_TTL", 900)) * time.Second,
	}
}

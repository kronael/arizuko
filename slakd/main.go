package main

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
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
			"typing": true, "edit": true, "like": true, "delete": true, "dislike": true, "post": true,
		},
		Start: func(_ context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("slack init failed", "err", err)
				return nil, nil, err
			}
			if cfg.StoreDir != "" {
				st, err := store.Open(cfg.StoreDir)
				if err != nil {
					slog.Error("slack store open failed", "dir", cfg.StoreDir, "err", err)
					return nil, nil, err
				}
				b.store = st
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
	// StoreDir is the directory containing messages.db (the parent of the
	// db file). Empty disables pane-session persistence. Derived from
	// DB_PATH (preferred) or DATA_DIR/store.
	StoreDir string
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
		StoreDir:      storeDirFromEnv(),
	}
}

// storeDirFromEnv resolves the dir containing messages.db from DB_PATH
// (preferred — explicit) or DATA_DIR/store (compose default). Returns
// "" when neither is set; pane persistence becomes a no-op.
func storeDirFromEnv() string {
	if p := chanlib.EnvOr("DB_PATH", ""); p != "" {
		return filepath.Dir(p)
	}
	if d := chanlib.EnvOr("DATA_DIR", ""); d != "" {
		return filepath.Join(d, "store")
	}
	return ""
}

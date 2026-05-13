package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
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
		Prefixes:      []string{"reddit:"},
		Caps: map[string]bool{
			"send_text": true, "fetch_history": true,
			"like": true, "dislike": true, "edit": true,
		},
		Start: func(ctx context.Context, router *chanlib.RouterClient) (http.Handler, func(), error) {
			rc, err := newRedditClient(cfg)
			if err != nil {
				slog.Error("reddit auth failed", "err", err)
				return nil, nil, err
			}
			rc.loadCursors()
			go rc.poll(ctx, router)
			return newServer(cfg, rc, rc.files, rc.isConnected, rc.LastInboundAt).handler(), nil, nil
		},
	})
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
	MaxFileBytes  int64
	PollInterval  time.Duration
}

func loadConfig() config {
	var subreddits []string
	for _, s := range strings.Split(chanlib.EnvOr("REDDIT_SUBREDDITS", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
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
		MaxFileBytes:  chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		PollInterval:  parseDuration(chanlib.EnvOr("REDDIT_POLL_INTERVAL", "5m")),
	}
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Second {
		return 5 * time.Minute
	}
	return d
}

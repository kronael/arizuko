package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
		Prefixes:      []string{"reddit:"},
		Caps:          map[string]bool{"send_text": true},
		Start: func(ctx context.Context, router *chanlib.RouterClient) (http.Handler, func(), error) {
			rc, err := newRedditClient(cfg)
			if err != nil {
				slog.Error("reddit auth failed", "err", err)
				return nil, nil, err
			}
			rc.loadCursors()
			go rc.poll(ctx, router)
			return newServer(cfg, rc, rc.files).handler(), nil, nil
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
		MaxFileBytes:  parseBytes(chanlib.EnvOr("MEDIA_MAX_FILE_BYTES", "20971520")),
	}
}

func parseBytes(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 20 * 1024 * 1024
	}
	return n
}

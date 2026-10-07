package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs reditd implements. Reddit has a true
// downvote so dislike is native. Forward/Quote/Repost return honest
// Unsupported hints; SendFile (3-step image upload) is not wired. post is
// real (text submit). The cap↔impl consistency test guards this.
var caps = map[string]bool{
	"send_text": true, "fetch_history": true, "post": true,
	"like": true, "dislike": true, "edit": true, "delete": true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"reddit:"},
		Caps:       caps,
		Start: func(ctx context.Context, router *chanlib.RouterClient) (http.Handler, func(), error) {
			// The store opens BEFORE the client, because the client's file
			// cache is built with it: a cache wired afterwards would answer
			// the ids of every attachment polled in between from memory
			// alone, and lose them on the next restart (BUGS L163).
			as, err := chanlib.OpenAdapterStore("reditd")
			if err != nil {
				slog.Error("adapter store open failed", "dir", cfg.DataDir, "err", err)
				return nil, nil, err
			}
			rc, err := newRedditClient(cfg, as.Chan)
			if err != nil {
				slog.Error("reddit auth failed", "err", err)
				as.Close() //nolint:errcheck // the start already failed
				return nil, nil, err
			}
			rc.loadCursors()
			go rc.poll(ctx, router)
			return newServer(cfg, rc, rc.files, as.Chan, rc.isConnected, rc.LastInboundAt).handler(),
				func() { _ = as.Close() }, nil
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
	ListenAddr    string
	ListenURL     string
	DataDir       string
	MediaMaxBytes int64
	PollInterval  time.Duration
}

func loadConfig() config {
	var subreddits []string
	for s := range strings.SplitSeq(chanlib.EnvOr("REDDIT_SUBREDDITS", ""), ",") {
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
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9006"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://reditd:9006"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/reditd"),
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
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

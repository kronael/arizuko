package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs bskyd implements. Forward/Dislike/Edit
// return honest Unsupported hints (no Bluesky primitive / appview rejects
// edits). The cap↔impl consistency test guards this.
var caps = map[string]bool{
	"send_text": true, "send_file": true, "fetch_history": true,
	"post": true, "like": true, "delete": true,
	"quote": true, "repost": true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"bluesky:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			bc, err := newBskyClient(cfg)
			if err != nil {
				slog.Error("bluesky auth failed", "err", err)
				return nil, nil, err
			}
			go bc.poll(ctx, rc)
			return newServer(cfg, bc, bc.isConnected, bc.LastInboundAt).handler(), nil, nil
		},
	})
}

type config struct {
	Name          string
	Identifier    string
	Password      string
	Service       string
	RouterURL     string
	ListenAddr    string
	ListenURL     string
	DataDir       string
	MediaMaxBytes int64
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "bluesky"),
		Identifier:    chanlib.MustEnv("BLUESKY_IDENTIFIER"),
		Password:      chanlib.MustEnv("BLUESKY_PASSWORD"),
		Service:       chanlib.EnvOr("BLUESKY_SERVICE", "https://bsky.social"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9005"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://bluesky:9005"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/bskyd"),
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
	}
}

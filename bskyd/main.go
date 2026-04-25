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
		Prefixes:      []string{"bluesky:"},
		Caps:          map[string]bool{"send_text": true, "fetch_history": true, "quote": true, "repost": true},
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
	ChannelSecret string
	ListenAddr    string
	ListenURL     string
	DataDir       string
	MaxFileBytes  int64
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "bluesky"),
		Identifier:    chanlib.MustEnv("BLUESKY_IDENTIFIER"),
		Password:      chanlib.MustEnv("BLUESKY_PASSWORD"),
		Service:       chanlib.EnvOr("BLUESKY_SERVICE", "https://bsky.social"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9005"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://bluesky:9005"),
		DataDir:       chanlib.EnvOr("DATA_DIR", "/srv/data/bskyd"),
		MaxFileBytes:  chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
	}
}

package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs mastd implements. Forward/Quote/Dislike
// return honest Unsupported hints (no Mastodon primitive); SendFile media
// upload is not wired. The cap↔impl consistency test guards this.
var caps = map[string]bool{
	"send_text": true, "fetch_history": true,
	"post": true, "like": true, "delete": true,
	"repost": true, "edit": true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"mastodon:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			mc, err := newMastoClient(cfg)
			if err != nil {
				slog.Error("mastodon connect failed", "err", err)
				return nil, nil, err
			}
			go mc.stream(ctx, rc)
			return newServer(cfg, mc, mc, mc.isConnected, mc.LastInboundAt).handler(), nil, nil
		},
	})
}

type config struct {
	Name          string
	InstanceURL   string
	AccessToken   string
	RouterURL     string
	ListenAddr    string
	ListenURL     string
	MaxFileBytes  int64
	FileCacheSize int
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "mastodon"),
		InstanceURL:   chanlib.MustEnv("MASTODON_INSTANCE_URL"),
		AccessToken:   chanlib.MustEnv("MASTODON_ACCESS_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9004"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://mastd:9004"),
		MaxFileBytes:  chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		FileCacheSize: chanlib.EnvInt("MASTODON_FILE_CACHE_SIZE", 1000),
	}
}

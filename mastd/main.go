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
		Prefixes:      []string{"mastodon:"},
		Caps:          map[string]bool{"send_text": true},
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			mc, err := newMastoClient(cfg)
			if err != nil {
				slog.Error("mastodon connect failed", "err", err)
				return nil, nil, err
			}
			go mc.stream(ctx, rc)
			return newServer(cfg, mc, mc).handler(), nil, nil
		},
	})
}

type config struct {
	Name          string
	InstanceURL   string
	AccessToken   string
	RouterURL     string
	ChannelSecret string
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
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9004"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://mastd:9004"),
		MaxFileBytes:  parseBytes(chanlib.EnvOr("MEDIA_MAX_FILE_BYTES", "20971520")),
		FileCacheSize: parseInt(chanlib.EnvOr("MASTODON_FILE_CACHE_SIZE", "1000")),
	}
}

func parseBytes(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 20 * 1024 * 1024
	}
	return n
}

func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 1000
	}
	return n
}

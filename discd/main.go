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
		Prefixes:      []string{"discord:"},
		Caps:          map[string]bool{"send_text": true, "send_file": true, "typing": true},
		Start: func(_ context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("discord auth failed", "err", err)
				return nil, nil, err
			}
			if err := b.start(rc); err != nil {
				slog.Error("discord connect failed", "err", err)
				return nil, nil, err
			}
			srv := newServer(cfg, b)
			b.files = &srv.files
			return srv.handler(), b.stop, nil
		},
	})
}

type config struct {
	Name, DiscordToken, RouterURL, ChannelSecret string
	ListenAddr, ListenURL, AssistantName         string
}

func loadConfig() config {
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "discord"),
		DiscordToken:  chanlib.MustEnv("DISCORD_BOT_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9002"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://discord:9002"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
	}
}

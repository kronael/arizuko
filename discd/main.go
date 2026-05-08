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
		Caps: map[string]bool{
			"send_text": true, "send_file": true, "typing": true, "fetch_history": true,
			"edit": true, "quote": true,
		},
		Start: func(_ context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("discord auth failed", "err", err)
				return nil, nil, err
			}
			// Wire b.files BEFORE opening the websocket. Events fire as
			// soon as Open returns; deferring this creates a race where an
			// inbound message with attachments dereferences nil b.files.
			srv := newServer(cfg, b, b.isConnected, b.LastInboundAt)
			b.files = srv.files
			if err := b.start(rc); err != nil {
				slog.Error("discord connect failed", "err", err)
				return nil, nil, err
			}
			return srv.handler(), b.stop, nil
		},
	})
}

type config struct {
	Name, DiscordToken, RouterURL, ChannelSecret string
	ListenAddr, ListenURL, AssistantName         string
	MediaMaxBytes                                int64
	UserMode                                     bool // true when authenticating as a user account, not a bot
}

func loadConfig() config {
	botToken := chanlib.EnvOr("DISCORD_BOT_TOKEN", "")
	userToken := chanlib.EnvOr("DISCORD_USER_TOKEN", "")
	if botToken == "" && userToken == "" {
		chanlib.MustEnv("DISCORD_BOT_TOKEN")
	}
	token := botToken
	userMode := false
	if userToken != "" {
		token = userToken
		userMode = true
	}
	return config{
		Name:          chanlib.EnvOr("CHANNEL_NAME", "discord"),
		DiscordToken:  token,
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9002"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://discord:9002"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		UserMode:      userMode,
	}
}

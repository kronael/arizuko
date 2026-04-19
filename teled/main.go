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
		Prefixes:      []string{"telegram:"},
		Caps: map[string]bool{
			"send_text":     true,
			"send_file":     true,
			"typing":        true,
			"fetch_history": true, // adapter replies "unsupported" at call time
		},
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("telegram auth failed", "err", err)
				return nil, nil, err
			}
			go b.poll(ctx, rc)
			return newServer(cfg, b).handler(), b.stop, nil
		},
	})
}

type config struct {
	Name, TelegramToken, RouterURL, ChannelSecret string
	ListenAddr, ListenURL, AssistantName          string
	StateFile                                     string
	MediaMaxBytes                                 int64
}

func loadConfig() config {
	dataDir := chanlib.EnvOr("DATA_DIR", "/srv/app/home")
	name := chanlib.EnvOr("CHANNEL_NAME", "telegram")
	return config{
		Name:          name,
		TelegramToken: chanlib.MustEnv("TELEGRAM_BOT_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ChannelSecret: chanlib.EnvOr("CHANNEL_SECRET", ""),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9001"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://telegram:9001"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		StateFile:     dataDir + "/teled-offset-" + name,
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
	}
}

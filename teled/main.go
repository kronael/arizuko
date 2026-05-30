package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/chanlib"
)

// caps advertises the gated verbs teled implements. quote/repost/dislike are
// absent: Telegram has no quote/repost-feed primitive, and dislike is an
// emoji reaction routed through like(emoji='👎') per platform convention —
// all three keep honest Unsupported hints in bot.go. fetch_history is absent
// too: the Bot API can't read past messages, so the gateway serves history
// from its own messages.db cache. The cap↔impl consistency test guards this.
var caps = map[string]bool{
	"send_text":  true,
	"send_file":  true,
	"send_voice": true,
	"typing":     true,
	"post":       true,
	"fwd":        true,
	"edit":       true,
	"like":       true,
	"delete":     true,
	"pin":        true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:          cfg.Name,
		RouterURL:     cfg.RouterURL,
		ChannelSecret: cfg.ChannelSecret,
		ListenAddr:    cfg.ListenAddr,
		ListenURL:     cfg.ListenURL,
		Prefixes:      []string{"telegram:"},
		Caps:          caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("telegram auth failed", "err", err)
				return nil, nil, err
			}
			// Derive b.cancel synchronously here — assigning it inside the poll
			// goroutine raced with stop() reading a still-nil b.cancel.
			pollCtx, cancel := context.WithCancel(ctx)
			b.cancel = cancel
			go b.poll(pollCtx, rc)
			return newServer(cfg, b, b.isConnected, b.LastInboundAt).handler(), b.stop, nil
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
		ChannelSecret: chanlib.EnvOr("TELED_CHANNEL_SECRET", chanlib.EnvOr("CHANNEL_SECRET", "")),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9001"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://telegram:9001"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		StateFile:     dataDir + "/teled-offset-" + name,
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
	}
}

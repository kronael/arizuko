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
	"thread_id":  true,
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
	// buttons is a render capability on send_text, not a verb: chanreg passes
	// SendRequest.Options through instead of folding them into the text.
	"buttons": true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"telegram:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("telegram auth failed", "err", err)
				return nil, nil, err
			}
			// Unreadable offset state is fatal: polling from 0 would re-deliver
			// Telegram's whole retained queue. chanlib.Run logs and exits.
			offset, err := b.loadOffset()
			if err != nil {
				return nil, nil, err
			}
			// Derive b.cancel synchronously here — assigning it inside the poll
			// goroutine raced with stop() reading a still-nil b.cancel.
			pollCtx, cancel := context.WithCancel(ctx)
			b.cancel = cancel
			// Outbound dedup is a delivery guarantee, not a nicety: without the
			// DB routd's re-send of a still-pending row posts a second message.
			as, err := chanlib.OpenAdapterStore("teled")
			if err != nil {
				slog.Error("adapter store open failed", "dir", cfg.DataDir, "err", err)
				return nil, nil, err
			}
			go b.poll(pollCtx, rc, offset)
			return newServer(cfg, b, as.Chan, b.isConnected, b.LastInboundAt).handler(),
				func() { b.stop(); _ = as.Close() }, nil
		},
	})
}

// teledStateDir is the DATA_DIR code default. It MUST equal the path
// template/services/teled.yml mounts and exports, the same "set in both places
// so neither drifts" rule the :8080 LISTEN_ADDR convention follows (root
// CLAUDE.md). The old default was the containerDataMount ROOT, so a missing env
// line dropped teled-offset-<name> at the top of the instance tree beside every
// daemon's database.
const teledStateDir = "/srv/app/home/store/teled"

type config struct {
	Name, TelegramToken, RouterURL       string
	ListenAddr, ListenURL, AssistantName string
	StateFile                            string
	DataDir                              string
	MediaMaxBytes                        int64
}

func loadConfig() config {
	dataDir := chanlib.EnvOr("DATA_DIR", teledStateDir)
	name := chanlib.EnvOr("CHANNEL_NAME", "telegram")
	return config{
		Name:          name,
		TelegramToken: chanlib.MustEnv("TELEGRAM_BOT_TOKEN"),
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    chanlib.EnvOr("LISTEN_ADDR", ":9001"),
		ListenURL:     chanlib.EnvOr("LISTEN_URL", "http://telegram:9001"),
		AssistantName: chanlib.EnvOr("ASSISTANT_NAME", ""),
		StateFile:     dataDir + "/teled-offset-" + name,
		DataDir:       dataDir,
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
	}
}

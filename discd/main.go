package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// caps advertises the gated verbs discd actually implements. Forward/Quote*/
// Repost/Dislike/Post return honest Unsupported hints (see bot.go) and are
// intentionally absent. (*Quote IS implemented via reply, so it stays.)
// The cap↔impl consistency test in main_test.go guards this from drifting.
var caps = map[string]bool{
	"send_text": true, "send_file": true, "send_voice": true, "typing": true, "fetch_history": true,
	"edit": true, "quote": true, "delete": true, "pin": true, "like": true,
	// buttons is a render capability on send_text, not a verb: chanreg passes
	// SendRequest.Options through instead of folding them into the text.
	"buttons": true, "thread_root": true, "thread_id": true,
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"discord:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("discord auth failed", "err", err)
				return nil, nil, err
			}
			// Wire b.files BEFORE opening the websocket. Events fire as
			// soon as Open returns; deferring this creates a race where an
			// inbound message with attachments dereferences nil b.files.
			// Outbound dedup is a delivery guarantee, not a nicety: without the
			// DB routd's re-send of a still-pending row posts a second message.
			as, err := chanlib.OpenAdapterStore("discd")
			if err != nil {
				slog.Error("adapter store open failed", "err", err)
				return nil, nil, err
			}
			// Inbound durability. The Discord gateway hands each event over
			// exactly once and offers no replay, so a routd that is restarting
			// used to drop the message (BUGS L25).
			rc.RetainInbound(ctx, as.Chan)
			srv := newServer(cfg, b, as.Chan, b.isConnected, b.LastInboundAt)
			b.files = srv.files
			if err := b.start(rc); err != nil {
				slog.Error("discord connect failed", "err", err)
				return nil, nil, err
			}
			return srv.handler(), func() { b.stop(); _ = as.Close() }, nil
		},
	})
}

type config struct {
	Name, DiscordToken, RouterURL        string
	ListenAddr, ListenURL, AssistantName string
	MediaMaxBytes                        int64
	UserMode                             bool // true when authenticating as a user account, not a bot
}

// parseToken reads DISCORD_TOKEN the way Discord's Authorization header is
// written: "Bot <token>" is a bot account, a bare token is a user account.
func parseToken(v string) (token string, userMode bool, err error) {
	t, isBot := strings.CutPrefix(v, "Bot ")
	if !isBot {
		t = v
	}
	if t = strings.TrimSpace(t); t == "" {
		return "", false, errors.New("DISCORD_TOKEN missing: set `Bot <token>` for a bot, a bare token for a user account")
	}
	return t, !isBot, nil
}

func loadConfig() config {
	token, userMode, err := parseToken(os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		slog.Error("required env var missing", "key", "DISCORD_TOKEN", "err", err)
		os.Exit(1)
	}
	return config{
		Name:          core.EnvOr("CHANNEL_NAME", "discord"),
		DiscordToken:  token,
		RouterURL:     chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:    core.EnvOr("LISTEN_ADDR", ":9002"),
		ListenURL:     core.EnvOr("LISTEN_URL", "http://discord:9002"),
		AssistantName: core.EnvOr("ASSISTANT_NAME", ""),
		MediaMaxBytes: chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		UserMode:      userMode,
	}
}

package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
)

// caps advertises the gated verbs slakd implements. Forward/Quote/Repost
// return honest Unsupported hints (Slack has no such primitive). pin covers
// Pin AND Unpin. dislike is a thin like(👎) delegation (true downvote absent,
// but the verb works). fetch_history is explicitly false — no history API
// wired. The cap↔impl consistency test guards this.
var caps = map[string]bool{
	"send_text": true, "send_file": true, "fetch_history": false,
	"typing": true, "edit": true, "like": true, "delete": true, "dislike": true, "post": true,
	"pin": true,
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
		Prefixes:   []string{"slack:"},
		Caps:       caps,
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b, err := newBot(cfg)
			if err != nil {
				slog.Error("slack init failed", "err", err)
				return nil, nil, err
			}
			// Pane reads hit routd's tables — routd OWNS pane_sessions in the
			// split topology (spec 5/16-mcp-rest-unification), and slakd writes panes there via routd
			// HTTP (paneWrite). Reading from the same store keeps read-after-write
			// coherent. store.OpenOwner is the door.
			st, err := store.OpenOwner(store.OwnerRoutd)
			if err != nil {
				slog.Error("slack store open failed", "err", err)
				return nil, nil, err
			}
			b.store = st
			// Outbound dedup is a delivery guarantee, not a nicety: without the
			// DB routd's re-send of a still-pending row posts a second message.
			as, err := chanlib.OpenAdapterStore("slakd")
			if err != nil {
				slog.Error("adapter store open failed", "err", err)
				return nil, nil, err
			}
			// Inbound durability. handleEvent answers Slack 200 BEFORE routd is
			// called (Slack wants the ack inside 3s and redelivers on non-2xx),
			// so Slack will never hand the message over again and a routd that
			// is restarting used to drop it (BUGS L25).
			rc.RetainInbound(ctx, as.Chan)
			b.useMark(rc, as.Chan)
			// A mark a crash left standing: find it by its record and clear it.
			go b.mark.Sweep(ctx)
			srv := newServer(cfg, b, as.Chan, b.isConnected, b.LastInboundAt)
			b.files = srv.files
			if err := b.start(ctx, rc); err != nil {
				slog.Error("slack auth.test failed", "err", err)
				return nil, nil, err
			}
			return srv.handler(), func() { b.stop(); _ = as.Close() }, nil
		},
	})
}

type config struct {
	Name          string
	BotToken      string
	SigningSecret string
	RouterURL     string
	ListenAddr    string
	ListenURL     string
	AssistantName string
	DataDir       string
	MediaMaxBytes int64
	CacheTTL      time.Duration
	// StaleSeconds: inbound silence beyond this marks the link dead (/health 503,
	// watchdog recovery). WatchdogEvery: how often the watchdog re-checks.
	// StaleFailLimit: consecutive stale checks (after re-probe) before os.Exit(1).
	StaleSeconds   int64
	WatchdogEvery  time.Duration
	StaleFailLimit int
}

func loadConfig() config {
	return config{
		Name:           chanlib.EnvOr("CHANNEL_NAME", "slack"),
		BotToken:       chanlib.MustEnv("SLACK_BOT_TOKEN"),
		SigningSecret:  chanlib.MustEnv("SLACK_SIGNING_SECRET"),
		RouterURL:      chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:     chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:      chanlib.EnvOr("LISTEN_URL", "http://slakd:8080"),
		DataDir:        chanlib.EnvOr("DATA_DIR", "/srv/app/home/store/slakd"),
		AssistantName:  chanlib.EnvOr("ASSISTANT_NAME", ""),
		MediaMaxBytes:  chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		CacheTTL:       time.Duration(chanlib.EnvInt("SLAKD_USERS_CACHE_TTL", 900)) * time.Second,
		StaleSeconds:   int64(chanlib.EnvInt("SLAKD_STALE_SECONDS", 300)),
		WatchdogEvery:  time.Duration(chanlib.EnvInt("SLAKD_WATCHDOG_SECONDS", 60)) * time.Second,
		StaleFailLimit: chanlib.EnvInt("SLAKD_STALE_FAIL_LIMIT", 5),
	}
}

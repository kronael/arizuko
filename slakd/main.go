package main

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
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
			if cfg.RoutdDSN != "" {
				// Pane reads hit routd.db — routd OWNS pane_sessions in the split
				// topology (spec 5/5), and slakd writes panes there via routd HTTP
				// (paneWrite). Reading from the same DB keeps read-after-write coherent.
				st, err := store.OpenRoutdAt(cfg.RoutdDSN)
				if err != nil {
					slog.Error("slack store open failed", "path", cfg.RoutdDSN, "err", err)
					return nil, nil, err
				}
				b.store = st
			}
			srv := newServer(cfg, b, b.isConnected, b.LastInboundAt)
			b.files = srv.files
			if err := b.start(ctx, rc); err != nil {
				slog.Error("slack auth.test failed", "err", err)
				return nil, nil, err
			}
			return srv.handler(), b.stop, nil
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
	MediaMaxBytes int64
	CacheTTL      time.Duration
	// StaleSeconds: inbound silence beyond this marks the link dead (/health 503,
	// watchdog recovery). WatchdogEvery: how often the watchdog re-checks.
	// StaleFailLimit: consecutive stale checks (after re-probe) before os.Exit(1).
	StaleSeconds   int64
	WatchdogEvery  time.Duration
	StaleFailLimit int
	// RoutdDSN is routd.db's path: DB_PATH, else this instance's own
	// store/<owner>/ layout under DATA_DIR.
	// Empty disables pane-session persistence. Derived from DB_PATH
	// (preferred — only its directory is used) or DATA_DIR/store.
	RoutdDSN string
}

func loadConfig() config {
	return config{
		Name:           chanlib.EnvOr("CHANNEL_NAME", "slack"),
		BotToken:       chanlib.MustEnv("SLACK_BOT_TOKEN"),
		SigningSecret:  chanlib.MustEnv("SLACK_SIGNING_SECRET"),
		RouterURL:      chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:     chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:      chanlib.EnvOr("LISTEN_URL", "http://slakd:8080"),
		AssistantName:  chanlib.EnvOr("ASSISTANT_NAME", ""),
		MediaMaxBytes:  chanlib.EnvBytes("MEDIA_MAX_FILE_BYTES", 20*1024*1024),
		CacheTTL:       time.Duration(chanlib.EnvInt("SLAKD_USERS_CACHE_TTL", 900)) * time.Second,
		RoutdDSN:       routdDSNFromEnv(),
		StaleSeconds:   int64(chanlib.EnvInt("SLAKD_STALE_SECONDS", 300)),
		WatchdogEvery:  time.Duration(chanlib.EnvInt("SLAKD_WATCHDOG_SECONDS", 60)) * time.Second,
		StaleFailLimit: chanlib.EnvInt("SLAKD_STALE_FAIL_LIMIT", 5),
	}
}

// routdDSNFromEnv resolves routd.db's path from DB_PATH (preferred — explicit,
// and the only DB path slakd's mount carries) or this instance's own
// store/<owner>/ layout under DATA_DIR. Returns "" when neither is set; pane
// persistence becomes a no-op.
func routdDSNFromEnv() string {
	if p := chanlib.EnvOr("DB_PATH", ""); p != "" {
		return p
	}
	if d := chanlib.EnvOr("DATA_DIR", ""); d != "" {
		return store.OwnerDBPath(filepath.Join(d, "store"), store.OwnerRoutd)
	}
	return ""
}

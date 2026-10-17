package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// capsFor advertises send_text only while the service-account key can post the
// reply; every other verb is Unsupported. Resolving, reopening and editing go
// through an MCP connector (specs/6/45-gdrive-comments-channel.md
// §"Decision 3 — one verb, the thread reply").
func capsFor(credentialed bool) map[string]bool {
	return map[string]bool{"send_text": credentialed}
}

func main() {
	cfg := loadConfig()
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"gdrive:"},
		Caps:       capsFor(cfg.KeyJSON != ""),
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			b := newBot(ctx, cfg)
			verifyCredential(ctx, b, rc, credentialRetryEvery)
			mux := chanlib.NewAdapterMux(cfg.Name, []string{"gdrive:"}, b, b.isConnected, b.LastInboundAt, nil)
			return mux, b.stop, nil
		},
	})
}

// credentialRetryEvery paces the re-ask of a key Google refused or never
// answered; it is the spec's default poll interval, the tick this retry
// belongs to once the poll loop exists.
const credentialRetryEvery = 30 * time.Second

// capSetter is the registration gdrid revises when its key turns out dead.
type capSetter interface {
	SetCaps(map[string]bool) error
}

// verifyCredential checks the key and, on any failure, keeps the daemon up.
// chanlib.Run exits on a Start error, and a daemon that exits at boot cycles
// the whole compose project (BUGS L406, L419), so one credential must never
// take every other daemon on the instance down. A key gates BOTH reading and
// replying here, so gdrid advertises no send_text and says why on its
// dashboard. rc.Register already ran, so routd would otherwise keep routing
// replies to an adapter that cannot post them.
//
// A key that is missing or will not parse is final until the operator fixes
// it. A key Google refused or never answered is asked again every `every`,
// and the first answer puts send_text back.
func verifyCredential(ctx context.Context, b *bot, rc capSetter, every time.Duration) {
	err := b.start(ctx)
	if err == nil {
		slog.Info("service account verified", "account", b.cred.account())
		return
	}
	slog.Error("service account credential unusable; gdrid runs without send_text and reads nothing until it is fixed",
		"err", err)
	setCaps(rc, capsFor(false))
	if b.cred.isRetryable() {
		go reacquire(ctx, b, rc, every)
	}
}

// reacquire verifies the key until Google accepts it, then puts send_text back.
func reacquire(ctx context.Context, b *bot, rc capSetter, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := b.cred.verify(ctx); err != nil {
				slog.Warn("service account still unusable; send_text stays withdrawn", "err", err)
				continue
			}
			slog.Info("service account verified; send_text is back on the registration",
				"account", b.cred.account())
			setCaps(rc, capsFor(true))
			return
		}
	}
}

func setCaps(rc capSetter, caps map[string]bool) {
	if err := rc.SetCaps(caps); err != nil {
		slog.Error("revising the registration failed; routd's view of gdrid's verbs is stale",
			"caps", caps, "err", err)
	}
}

type bot struct {
	chanlib.NoSocial
	chanlib.NoFileSender
	chanlib.NoVoiceSender

	cred          *credential
	connected     atomic.Bool
	lastInboundAt atomic.Int64
}

func newBot(ctx context.Context, cfg config) *bot {
	return &bot{cred: newCredential(ctx, cfg)}
}

func (b *bot) isConnected() bool    { return b.connected.Load() }
func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

// start verifies the credential. gdrid is connected whatever the answer: the
// key gates one daemon's reading and replying, never its liveness.
func (b *bot) start(ctx context.Context) error {
	defer b.connected.Store(true)
	return b.cred.verify(ctx)
}

func (b *bot) stop() { b.connected.Store(false) }

// Send has no reply path yet.
func (b *bot) Send(chanlib.SendRequest) (string, error) {
	return "", fmt.Errorf("%w: gdrid cannot post a reply", chanlib.ErrInvalidRequest)
}

func (b *bot) Typing(string, bool) {}

// DashSections shows the credential first: the account's address is what an
// operator shares a document with, and a refusal is why nothing arrives.
func (b *bot) DashSections() []chanlib.DashSection {
	account := b.cred.account()
	if account == "" {
		account = "unknown until the key is accepted"
	}
	return []chanlib.DashSection{{
		Title: "Service account",
		Rows: [][2]string{
			{"credential", b.cred.status()},
			{"share documents with", account},
		},
	}}
}

type config struct {
	Name       string
	KeyJSON    string
	Subject    string
	APIBase    string
	RouterURL  string
	ListenAddr string
	ListenURL  string
}

func loadConfig() config {
	return config{
		Name:       chanlib.EnvOr("CHANNEL_NAME", "gdrive"),
		KeyJSON:    chanlib.EnvOr("GDRIVE_SA_KEY_JSON", ""),
		Subject:    chanlib.EnvOr("GDRIVE_IMPERSONATE", ""),
		APIBase:    chanlib.EnvOr("GDRIVE_API_URL", "https://www.googleapis.com"),
		RouterURL:  chanlib.MustEnv("ROUTER_URL"),
		ListenAddr: chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:  chanlib.EnvOr("LISTEN_URL", "http://gdrid:8080"),
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
)

// capsFor advertises send_text only while the service-account key can post the
// reply; every other verb is Unsupported. Resolving, reopening and editing go
// through an MCP connector (specs/7/50-gdrive-comments-channel.md
// §"Decision 3 — one verb, the thread reply").
func capsFor(credentialed bool) map[string]bool {
	return map[string]bool{"send_text": credentialed}
}

func main() {
	cfg := loadConfig()
	b, err := prepareBot(context.Background(), cfg)
	chanlib.Run(chanlib.RunOpts{
		Name:       cfg.Name,
		RouterURL:  cfg.RouterURL,
		ListenAddr: cfg.ListenAddr,
		ListenURL:  cfg.ListenURL,
		Prefixes:   []string{"gdrive:"},
		Caps:       capsFor(err == nil),
		Start: func(ctx context.Context, rc *chanlib.RouterClient) (http.Handler, func(), error) {
			if err != nil {
				slog.Error("service account credential unusable", "err", err)
			}
			if b.cred.src != nil {
				go reacquire(ctx, b, rc, credentialRetryEvery)
			}
			as, openErr := chanlib.OpenAdapterStore("googd")
			if openErr != nil {
				return nil, nil, openErr
			}
			st, openErr := store.OpenOwner(store.OwnerRoutd)
			if openErr != nil {
				_ = as.Close()
				return nil, nil, openErr
			}
			if openErr = as.Driver.CreateEntity(ctx, pollEntity); openErr != nil {
				_ = st.Close()
				_ = as.Close()
				return nil, nil, openErr
			}
			pollCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			p := &poller{b: b, bindings: st, db: as.Driver, router: rc, name: cfg.Name, interval: pollInterval(), files: make(map[string]*filePoll)}
			b.poll = p
			go func() { defer close(done); p.pollLoop(pollCtx) }()
			mux := chanlib.NewAdapterMux(cfg.Name, []string{"gdrive:"}, b, b.isConnected, b.LastInboundAt, as.Chan)
			mux.Handle("/google/", b.passthrough(cfg.ConnectorToken))
			return mux, func() { cancel(); <-done; b.stop(); _ = st.Close(); _ = as.Close() }, nil
		},
	})
}

func prepareBot(ctx context.Context, cfg config) (*bot, error) {
	b := newBot(ctx, cfg)
	return b, b.start(ctx)
}

// credentialRetryEvery paces the re-ask of a key Google refused or never
// answered; it is the spec's default poll interval, the tick this retry
// belongs to once the poll loop exists.
const credentialRetryEvery = 30 * time.Second

// capSetter is the registration googd revises when its key turns out dead.
type capSetter interface {
	SetCaps(map[string]bool) error
}

// verifyCredential checks the key and, on any failure, keeps the daemon up.
// chanlib.Run exits on a Start error, and a daemon that exits at boot cycles
// the whole compose project (BUGS L406, L419), so one credential must never
// take every other daemon on the instance down. A key gates BOTH reading and
// replying here, so googd advertises no send_text and says why on its
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
		if b.cred.src != nil {
			go reacquire(ctx, b, rc, every)
		}
		return
	}
	slog.Error("service account credential unusable; googd runs without send_text and reads nothing until it is fixed",
		"err", err)
	setCaps(rc, capsFor(false))
	if b.cred.isRetryable() {
		go reacquire(ctx, b, rc, every)
	}
}

// reacquire refreshes API state and updates the Drive capability.
func reacquire(ctx context.Context, b *bot, rc capSetter, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	registered := b.cred.usable()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := b.cred.verify(ctx); err != nil {
				if registered {
					setCaps(rc, capsFor(false))
					registered = false
				}
				slog.Warn("service account still unusable; send_text stays withdrawn", "err", err)
				continue
			}
			if !registered {
				slog.Info("service account verified; send_text is back on the registration",
					"account", b.cred.account())
				setCaps(rc, capsFor(true))
				registered = true
			}
		}
	}
}

func setCaps(rc capSetter, caps map[string]bool) {
	if err := rc.SetCaps(caps); err != nil {
		slog.Error("revising the registration failed; routd's view of googd's verbs is stale",
			"caps", caps, "err", err)
	}
}

type bot struct {
	chanlib.NoSocial
	chanlib.NoFileSender
	chanlib.NoVoiceSender

	cred          *credential
	poll          *poller
	connected     atomic.Bool
	lastInboundAt atomic.Int64
}

func newBot(ctx context.Context, cfg config) *bot {
	return &bot{cred: newCredential(ctx, cfg)}
}

func (b *bot) isConnected() bool    { return b.connected.Load() }
func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

// start verifies the credential. googd is connected whatever the answer: the
// key gates one daemon's reading and replying, never its liveness.
func (b *bot) start(ctx context.Context) error {
	defer b.connected.Store(true)
	return b.cred.verify(ctx)
}

func (b *bot) stop() { b.connected.Store(false) }

// Send posts a reply to the thread named by the JID.
func (b *bot) Send(msg chanlib.SendRequest) (string, error) {
	jid, ok := chanlib.ParseGDriveJID(msg.ChatJID)
	if !ok || jid.CommentID == "" {
		return "", fmt.Errorf("%w: thread JID required", chanlib.ErrInvalidRequest)
	}
	if b.cred.http == nil {
		return "", fmt.Errorf("%w: credential unavailable", chanlib.ErrNotDelivered)
	}
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{msg.Content})
	if err != nil {
		return "", err
	}
	url := b.cred.apiBase + "/drive/v3/files/" + jid.FileID + "/comments/" + jid.CommentID + "/replies?fields=id"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.cred.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("drive reply: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", fmt.Errorf("%w: drive reply: %s", chanlib.ErrNotDelivered, resp.Status)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return "", fmt.Errorf("%w: drive reply: %s", chanlib.ErrPermanent, resp.Status)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("drive reply: %s", resp.Status)
	}
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return "", fmt.Errorf("drive reply response: %w", err)
	}
	if reply.ID == "" {
		return "", fmt.Errorf("drive reply response has no id")
	}
	return "gd-r" + jid.FileID + "." + jid.CommentID + "." + reply.ID, nil
}

func (b *bot) Typing(string, bool) {}

// DashSections shows the credential first: the account's address is what an
// operator shares a document with, and a refusal is why nothing arrives.
func (b *bot) DashSections() []chanlib.DashSection {
	account := b.cred.account()
	if account == "" {
		account = "unknown until the key is accepted"
	}
	b.cred.mu.Lock()
	drive, sheets := b.cred.driveAPI, b.cred.sheetsAPI
	b.cred.mu.Unlock()
	sections := []chanlib.DashSection{{
		Title: "Service account",
		Rows: [][2]string{
			{"credential", b.cred.status()},
			{"share documents with", account},
		},
	}}
	sections = append(sections, chanlib.DashSection{Title: "APIs", Rows: [][2]string{
		{"Drive", apiLine(drive, "drive.googleapis.com")},
		{"Sheets", apiLine(sheets, "sheets.googleapis.com")},
	}})
	if b.poll != nil {
		sections = append(sections, b.poll.dashSection())
	}
	return sections
}

func apiLine(state apiState, service string) string {
	if state.state == "" {
		state.state = "unknown"
	}
	return state.state + "; " + state.reason + "; https://console.cloud.google.com/apis/library/" + service
}

type config struct {
	Name           string
	KeyJSON        string
	Subject        string
	APIBase        string
	SheetsAPIBase  string
	ConnectorToken string
	RouterURL      string
	ListenAddr     string
	ListenURL      string
}

func loadConfig() config {
	return config{
		Name:           chanlib.EnvOr("CHANNEL_NAME", "gdrive"),
		KeyJSON:        chanlib.EnvOr("GDRIVE_SA_KEY_JSON", ""),
		Subject:        chanlib.EnvOr("GDRIVE_IMPERSONATE", ""),
		APIBase:        chanlib.EnvOr("GDRIVE_API_URL", "https://www.googleapis.com"),
		SheetsAPIBase:  chanlib.EnvOr("SHEETS_API_URL", "https://sheets.googleapis.com"),
		ConnectorToken: chanlib.EnvOr("GOOGD_CONNECTOR_TOKEN", ""),
		RouterURL:      chanlib.MustEnv("ROUTER_URL"),
		ListenAddr:     chanlib.EnvOr("LISTEN_ADDR", ":8080"),
		ListenURL:      chanlib.EnvOr("LISTEN_URL", "http://googd:8080"),
	}
}

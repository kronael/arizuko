package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

type bot struct {
	chanlib.NoSocial
	chanlib.NoFileSender
	chanlib.NoVoiceSender

	cfg  config
	http *http.Client

	login         atomic.Value
	connected     atomic.Bool
	lastInboundAt atomic.Int64
	deliveries    sync.Map
}

func newBot(cfg config) *bot {
	return &bot{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
}

func (b *bot) isConnected() bool    { return b.connected.Load() }
func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

// Login is the account GITHUB_TOKEN acts as; "" without a token. Ingest drops
// events this login sends so the agent's own comment cannot start a turn.
func (b *bot) Login() string {
	v, _ := b.login.Load().(string)
	return v
}

func (b *bot) markDelivery(source string, at time.Time) {
	b.lastInboundAt.Store(at.Unix())
	b.deliveries.Store(source, at.Unix())
}

// start learns the token's login (GET /user) so self-authored comments are
// recognised at ingest. No token → ingress only, nothing to learn.
func (b *bot) start(ctx context.Context) error {
	defer b.connected.Store(true)
	if b.cfg.Token == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", b.cfg.APIBase+"/user", nil)
	if err != nil {
		return err
	}
	b.decorate(req)
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	// body fully read below
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github /user: %s", resp.Status)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return err
	}
	if u.Login == "" {
		return fmt.Errorf("github /user: empty login")
	}
	b.login.Store(u.Login)
	slog.Info("github token verified", "login", u.Login)
	return nil
}

func (b *bot) stop() { b.connected.Store(false) }

func (b *bot) decorate(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", chanlib.UserAgent)
}

// Send posts a comment on the issue or PR the JID names. GitHub keeps PR and
// issue comments on one endpoint; a ThreadID that is a review topic
// (GitHubJID.ReviewTopic) posts under that inline review thread instead, and
// GitHub's refusal of a missing or foreign root is returned as is. Returns
// "gh-c<id>", the id inbound uses for the same comment, so a later reply_to
// resolves to this row.
func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	j, ok := chanlib.ParseGitHubJID(req.ChatJID)
	if !ok {
		return "", fmt.Errorf("%w: malformed github jid %q", chanlib.ErrInvalidRequest, req.ChatJID)
	}
	if j.Kind == "" {
		return "", fmt.Errorf("%w: %s has no comment thread; reply on a pr/<n> or issue/<n> jid",
			chanlib.ErrInvalidRequest, req.ChatJID)
	}
	if b.cfg.Token == "" {
		return "", fmt.Errorf("%w: GITHUB_TOKEN unset; post through the github connector", chanlib.ErrPermanent)
	}
	body, _ := json.Marshal(map[string]string{"body": req.Content})
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", b.cfg.APIBase, j.Owner, j.Repo, j.Number)
	if root, ok := j.ReviewRoot(req.ThreadID); ok {
		url = fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments/%d/replies", b.cfg.APIBase, j.Owner, j.Repo, j.Number, root)
	}
	hreq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	b.decorate(hreq)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("%w: %v", chanlib.ErrNotDelivered, err)
	}
	// body fully read below
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusCreated:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: github %s: %s", chanlib.ErrNotDelivered, resp.Status, bytes.TrimSpace(raw))
	default:
		return "", fmt.Errorf("%w: github %s: %s", chanlib.ErrPermanent, resp.Status, bytes.TrimSpace(raw))
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == 0 {
		return "", fmt.Errorf("github comment response without id: %s", bytes.TrimSpace(raw))
	}
	return "gh-c" + strconv.FormatInt(created.ID, 10), nil
}

func (b *bot) Typing(string, bool) {}

// DashSections lists each source's last accepted delivery, the one fact an
// operator wiring a webhook needs from this page.
func (b *bot) DashSections() []chanlib.DashSection {
	var rows [][2]string
	b.deliveries.Range(func(k, v any) bool {
		at, _ := v.(int64)
		rows = append(rows, [2]string{k.(string), time.Unix(at, 0).UTC().Format(time.RFC3339)})
		return true
	})
	if len(rows) == 0 {
		rows = [][2]string{{"deliveries", "none since start"}}
	}
	return []chanlib.DashSection{{Title: "Last delivery per source", Rows: rows}}
}

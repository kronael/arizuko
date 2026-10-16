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
	"strings"
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

// reviewIDPrefix namespaces an inline review comment's id. GitHub keeps review
// comments and issue comments in SEPARATE id spaces behind separate endpoints,
// so a bare number cannot say which one it names and the id says it instead.
const reviewIDPrefix = "gh-rc"

// reviewCommentID answers the review comment a reply_to names, and ok=false
// for an issue comment, a review, or no reply target at all.
func reviewCommentID(replyTo string) (int64, bool) {
	rest, found := strings.CutPrefix(replyTo, reviewIDPrefix)
	if !found {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

// call makes one GitHub request and classifies the refusal the way the outbound
// sweep reads it: 429 and 5xx are worth another attempt, every other status is
// permanent. It is the ONE place gitd turns a status into an error, so the reply
// and the thread lookup cannot read the same status differently.
func (b *bot) call(method, url string, body []byte, want int) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	hreq, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	b.decorate(hreq)
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", chanlib.ErrNotDelivered, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == want:
		return raw, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: github %s: %s", chanlib.ErrNotDelivered, resp.Status, bytes.TrimSpace(raw))
	default:
		return nil, fmt.Errorf("%w: github %s: %s", chanlib.ErrPermanent, resp.Status, bytes.TrimSpace(raw))
	}
}

// threadRoot answers the comment that OPENED the inline review thread id sits
// in. GitHub takes only that comment as a reply target — "replies to replies
// are not supported" — while the id routd hands back is whichever comment
// anchored the turn, a reply as often as the root.
func (b *bot) threadRoot(j chanlib.GitHubJID, id int64) (int64, error) {
	raw, err := b.call("GET",
		fmt.Sprintf("%s/repos/%s/%s/pulls/comments/%d", b.cfg.APIBase, j.Owner, j.Repo, id),
		nil, http.StatusOK)
	if err != nil {
		return 0, err
	}
	var c struct {
		InReplyToID int64 `json:"in_reply_to_id"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0, fmt.Errorf("%w: github review comment %d: %v", chanlib.ErrPermanent, id, err)
	}
	if c.InReplyToID != 0 {
		return c.InReplyToID, nil
	}
	return id, nil
}

// Send posts the agent's reply as a comment, and ReplyTo decides where. A
// review comment's id answers under that inline thread through the review-reply
// endpoint, so a reviewer reading the diff sees the answer on its own code
// line; every other reply uses the issue-comments endpoint, which GitHub shares
// between PR and issue conversations. GitHub's refusal of a missing or foreign
// thread is returned, never redirected to the conversation tab, which would
// hide it. Returns the id inbound uses for the same comment, so a later
// reply_to resolves to this row.
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
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", b.cfg.APIBase, j.Owner, j.Repo, j.Number)
	prefix := "gh-c"
	if id, ok := reviewCommentID(req.ReplyTo); ok && j.Kind == "pr" {
		root, err := b.threadRoot(j, id)
		if err != nil {
			return "", err
		}
		url = fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments/%d/replies",
			b.cfg.APIBase, j.Owner, j.Repo, j.Number, root)
		prefix = reviewIDPrefix
	}
	body, _ := json.Marshal(map[string]string{"body": req.Content})
	raw, err := b.call("POST", url, body, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == 0 {
		return "", fmt.Errorf("github comment response without id: %s", bytes.TrimSpace(raw))
	}
	return prefix + strconv.FormatInt(created.ID, 10), nil
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

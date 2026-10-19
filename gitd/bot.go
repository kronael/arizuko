package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kronael/arizuko/chanlib"
)

type bot struct {
	chanlib.NoSocial
	chanlib.NoVoiceSender

	cfg  config
	http *http.Client

	login         atomic.Value
	connected     atomic.Bool
	lastInboundAt atomic.Int64
	deliveries    sync.Map
	mark          *chanlib.AckMark
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
		slog.Warn(noLoginConsequence("GITHUB_TOKEN is unset"))
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
	// body read below
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return statusErr(resp, raw)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return err
	}
	if u.Login == "" {
		return fmt.Errorf("github /user: empty login")
	}
	b.login.Store(u.Login)
	slog.Info("github token verified", "login", u.Login)
	return nil
}

// noLoginConsequence names what an empty bot login costs. Seeded routes
// trigger on mentions only, and a mention needs the login, so the agent is
// never woken while the login is empty.
func noLoginConsequence(cause string) string {
	return cause + ": gitd has no bot login, so no event can be a mention of the agent or an assignment to it; " +
		"every event is observed and the agent is never woken"
}

func (b *bot) stop() {
	b.connected.Store(false)
	if b.mark != nil {
		b.mark.Stop()
	}
}

func (b *bot) decorate(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", chanlib.UserAgent)
}

// reviewIDPrefix namespaces an inline review comment's id. GitHub keeps review
// comments and issue comments in SEPARATE id spaces behind separate endpoints,
// so a bare number cannot say which one it names and the id says it instead.
const reviewIDPrefix = "gh-rc"

// issueIDPrefix namespaces an issue or PR conversation comment's id.
const issueIDPrefix = "gh-c"

// reviewCommentID answers the review comment a reply_to names, and ok=false
// for an issue comment, a review, or no reply target at all.
func reviewCommentID(replyTo string) (int64, bool) {
	return prefixedID(reviewIDPrefix, replyTo)
}

// issueCommentID answers the conversation comment an inbound id names, and
// ok=false for a review comment, a review, or any other id.
func issueCommentID(msgID string) (int64, bool) {
	return prefixedID(issueIDPrefix, msgID)
}

func prefixedID(prefix, id string) (int64, bool) {
	rest, found := strings.CutPrefix(id, prefix)
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil && n > 0
}

// isRateLimited answers whether GitHub refused this response to slow the client
// down rather than for good. Both the primary and the secondary limit answer
// 403 as well as 429, and the signals the REST reference names are a
// retry-after header or x-ratelimit-remaining: 0
// (docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
func isRateLimited(h http.Header) bool {
	return h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0"
}

// statusErr turns a status GitHub answered into the error its reader acts on:
// 429, 5xx and a rate-limited 403 are worth another attempt, every other
// status is permanent. It is the ONE place gitd turns a status into an error,
// so the reply, the thread lookup and the boot probe cannot read the same
// status differently.
func statusErr(resp *http.Response, raw []byte) error {
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
		(resp.StatusCode == http.StatusForbidden && isRateLimited(resp.Header)) {
		return fmt.Errorf("%w: github %s: %s", chanlib.ErrNotDelivered, resp.Status, bytes.TrimSpace(raw))
	}
	return fmt.Errorf("%w: github %s: %s", chanlib.ErrPermanent, resp.Status, bytes.TrimSpace(raw))
}

// call makes one GitHub request and returns its body, or the refusal
// statusErr classifies.
func (b *bot) call(method, url string, body []byte, want []int) ([]byte, error) {
	return b.callContext(context.Background(), method, url, body, want)
}

func (b *bot) callContext(ctx context.Context, method, url string, body []byte, want []int) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	b.decorate(hreq)
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(hreq)
	if err != nil {
		// A GET creates nothing, so a lost response cannot have posted a
		// comment and the dedup claim is free to release. On a POST the client
		// timeout fires after the request body went out, so a lost response may
		// have created the comment: releasing the claim would post a second one
		// on a public PR. teled and discd leave a transport error unclassified
		// for the same reason; the claim stays and routd dead-letters visibly.
		if method == http.MethodGet {
			return nil, fmt.Errorf("%w: github request: %w", chanlib.ErrNotDelivered, err)
		}
		return nil, fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !slices.Contains(want, resp.StatusCode) {
		return nil, statusErr(resp, raw)
	}
	return raw, nil
}

// threadRoot answers the comment that OPENED the inline review thread id sits
// in. GitHub takes only that comment as a reply target — "replies to replies
// are not supported" — while the id routd hands back is whichever comment
// anchored the turn, a reply as often as the root.
func (b *bot) threadRoot(j chanlib.GitHubJID, id int64) (int64, error) {
	raw, err := b.call("GET",
		fmt.Sprintf("%s/repos/%s/%s/pulls/comments/%d", b.cfg.APIBase, j.Owner, j.Repo, id),
		nil, []int{http.StatusOK})
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
	prefix := issueIDPrefix
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
	raw, err := b.call("POST", url, body, []int{http.StatusCreated})
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

const maxCommentChars = 65536

const uploadHint = "commit it to a branch with the github connector and link it here"

// SendFile posts a text file as a comment: the caption, the file name, then
// the content in a fenced block. GitHub has no API that attaches a file to a
// comment, so a file that is not text, or does not fit the comment cap, is
// refused before any request with the connector tool that can hold it.
func (b *bot) SendFile(jid, path, name, caption, replyTo, _, _ string) (string, error) {
	if b.cfg.Token == "" {
		return "", chanlib.Unsupported("send_file", "github", "GITHUB_TOKEN unset; "+uploadHint)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %v", chanlib.ErrInvalidRequest, path, err)
	}
	if name == "" {
		name = path
	}
	name = filepath.Base(name)
	if !utf8.Valid(data) || slices.Contains(data, 0) {
		return "", chanlib.Unsupported("send_file", "github",
			"a comment holds text only, and "+name+" is not text; "+uploadHint)
	}
	body := fileComment(name, caption, string(data))
	if n := utf8.RuneCountInString(body); n > maxCommentChars {
		return "", fmt.Errorf("%w: %s makes a %d character comment and GitHub caps a comment at %d; %s",
			chanlib.ErrInvalidRequest, name, n, maxCommentChars, uploadHint)
	}
	return b.Send(chanlib.SendRequest{ChatJID: jid, Content: body, ReplyTo: replyTo})
}

func fileComment(name, caption, text string) string {
	var sb strings.Builder
	if caption != "" {
		sb.WriteString(caption + "\n\n")
	}
	// One sanitisation, two readers: the label and the fence's language hint both
	// come from it, so neither can carry a backtick or a newline out of the name.
	label := strings.NewReplacer("`", "'", "\n", " ", "\r", " ").Replace(name)
	sb.WriteString("`" + label + "`\n\n")
	fence := strings.Repeat("`", max(3, longestBackticks(text)+1))
	lang := strings.ToLower(strings.TrimPrefix(filepath.Ext(label), "."))
	sb.WriteString(fence + lang + "\n" + text)
	if !strings.HasSuffix(text, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(fence)
	return sb.String()
}

func longestBackticks(text string) int {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}

// commentPath answers the API path of the comment id names, and ok=false for
// any id gitd did not mint as a comment: a bare number, a review (gh-r) or an
// empty id. The two comment kinds live in separate id spaces behind separate
// endpoints, the same split reactionsPath makes; the id prefix picks the path.
func commentPath(j chanlib.GitHubJID, id string) (string, bool) {
	if n, ok := reviewCommentID(id); ok {
		return fmt.Sprintf("/repos/%s/%s/pulls/comments/%d", j.Owner, j.Repo, n), true
	}
	if n, ok := issueCommentID(id); ok {
		return fmt.Sprintf("/repos/%s/%s/issues/comments/%d", j.Owner, j.Repo, n), true
	}
	return "", false
}

// Edit replaces the text of a comment gitd wrote, so a progress message is one
// comment changed in place (specs/6/46-turn-progress.md). The id grammar alone
// cannot say who wrote a comment, and a token with write access may edit
// anyone's, so the comment is read first and refused unless its author is the
// login gitd acts as. A refusal is permanent: no retry makes a comment gitd did
// not write gitd's.
func (b *bot) Edit(req chanlib.EditRequest) error {
	if b.cfg.Token == "" {
		// Ingress-only: capsFor withdraws edit with the token, and the verb
		// says the same, so CapImplReport sees one answer both ways.
		return chanlib.ErrUnsupported
	}
	j, ok := chanlib.ParseGitHubJID(req.ChatJID)
	if !ok || j.Kind == "" {
		return fmt.Errorf("%w: %q has no comment thread to edit in", chanlib.ErrInvalidRequest, req.ChatJID)
	}
	path, ok := commentPath(j, req.TargetID)
	if !ok {
		return fmt.Errorf("%w: %q names no comment gitd wrote (want gh-c<id> or gh-rc<id>)",
			chanlib.ErrInvalidRequest, req.TargetID)
	}
	me := b.Login()
	if me == "" {
		return fmt.Errorf("%w: gitd has not learned its own login; cannot tell its comments from others", chanlib.ErrPermanent)
	}
	raw, err := b.call("GET", b.cfg.APIBase+path, nil, []int{http.StatusOK})
	if err != nil {
		return err
	}
	var c struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("%w: github comment %s: %v", chanlib.ErrPermanent, req.TargetID, err)
	}
	if c.User == nil || !strings.EqualFold(c.User.Login, me) {
		return fmt.Errorf("%w: %s was not written by %s; gitd edits only its own comments",
			chanlib.ErrPermanent, req.TargetID, me)
	}
	body, _ := json.Marshal(map[string]string{"body": req.Content})
	_, err = b.call("PATCH", b.cfg.APIBase+path, body, []int{http.StatusOK})
	return err
}

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

package chanreg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// ErrUnsupported is returned when an adapter responds 501 to a social
// verb. Aliased to chanlib.ErrUnsupported so errors.Is chains through
// *chanlib.UnsupportedError values too.
var ErrUnsupported = chanlib.ErrUnsupported

// decodeUnsupported reads a 501 body into a structured *chanlib.UnsupportedError.
// On parse failure or empty body, returns plain ErrUnsupported.
func decodeUnsupported(body io.Reader, fallbackTool, fallbackPlatform string) error {
	var b struct {
		Tool     string `json:"tool"`
		Platform string `json:"platform"`
		Hint     string `json:"hint"`
	}
	if body == nil {
		return ErrUnsupported
	}
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		return ErrUnsupported
	}
	if b.Hint == "" && b.Tool == "" && b.Platform == "" {
		return ErrUnsupported
	}
	if b.Tool == "" {
		b.Tool = fallbackTool
	}
	if b.Platform == "" {
		b.Platform = fallbackPlatform
	}
	return &chanlib.UnsupportedError{Tool: b.Tool, Platform: b.Platform, Hint: b.Hint}
}

const maxOutbox = 1000

// maxSendAttempts bounds outbox retries per message so a permanently-dead jid
// (group deleted, bot kicked) self-evicts instead of requeuing forever and
// filling the outbox → dropping messages for ALL groups on the channel.
const maxSendAttempts = 5

// errPermanent marks a 4xx adapter response — a permanent failure (bad request,
// chat gone, bad emoji) the caller can't fix by retrying. It is surfaced to the
// agent and never enqueued; a transient 5xx/network error is retried instead.
var errPermanent = errors.New("permanent")

// classifyStatus wraps a non-200 adapter status: 4xx → errPermanent, else plain.
func classifyStatus(status int) error {
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w: status %d", errPermanent, status)
	}
	return fmt.Errorf("status %d", status)
}

type HTTPChannel struct {
	entry *Entry
	// bearer yields the credential presented to the adapter on every call: routd's
	// service:routd ES256 JWT (spec 5/1), or nothing in local dev (no AUTHD_URL).
	// Never nil — NewHTTPChannel installs a getter.
	bearer func(context.Context) (string, error)
	client *http.Client

	mu     sync.RWMutex
	outbox []outMsg
}

type outMsg struct {
	JID         string
	Content     string
	ReplyTo     string
	ThreadID    string
	ThreadRoot  string
	TurnID      string
	IsFile      bool
	Path        string
	Name        string
	Caption     string
	FileReplyTo string
	Attempts    int
}

// NewHTTPChannel builds the routd-side egress client for an adapter. bearer
// yields the service:routd token presented on every call. A nil bearer means
// "no auth header" (local dev with no AUTHD_URL, or single-process tests).
func NewHTTPChannel(e *Entry, bearer func(context.Context) (string, error)) *HTTPChannel {
	if bearer == nil {
		bearer = func(context.Context) (string, error) { return "", nil }
	}
	return &HTTPChannel{
		entry:  e,
		bearer: bearer,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// authHeader sets Authorization from the bearer getter when it yields a token.
// A getter error is logged and the request goes out unauthenticated → the
// adapter returns 401, surfacing the auth failure rather than masking it.
func (h *HTTPChannel) authHeader(ctx context.Context, req *http.Request) {
	tok, err := h.bearer(ctx)
	if err != nil {
		slog.Error("channel egress: bearer token unavailable", "channel", h.entry.Name, "err", err)
		return
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func (h *HTTPChannel) Name() string { return h.entry.Name }

func (h *HTTPChannel) Connect(_ context.Context) error { return nil }

func (h *HTTPChannel) Owns(jid string) bool { return h.entry.Owns(jid) }

func (h *HTTPChannel) Send(jid, text, replyTo, threadID, threadRoot, turnID string) (string, error) {
	return h.SendCtx(context.Background(), jid, text, replyTo, threadID, threadRoot, turnID)
}

func (h *HTTPChannel) SendCtx(ctx context.Context, jid, text, replyTo, threadID, threadRoot, turnID string) (string, error) {
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	m := outMsg{JID: jid, Content: text, ReplyTo: replyTo, ThreadID: threadID, ThreadRoot: threadRoot, TurnID: turnID}
	id, err := h.trySend(ctx, m)
	if err != nil && !errors.Is(err, errPermanent) {
		h.enqueue(m) // transient failure enters the outbox; DrainOutbox owns the retry cap
	}
	return id, err // permanent (4xx) errors surface to the agent, never retried
}

// trySend performs ONE delivery attempt for m without touching the outbox, so
// the direct-send path and DrainOutbox each own their own enqueue/retry policy
// (the direct path enqueues on failure; the drain re-enqueues with a bounded
// attempt counter so a dead jid self-evicts).
func (h *HTTPChannel) trySend(ctx context.Context, m outMsg) (string, error) {
	if m.IsFile {
		if !h.entry.HasCap("send_file") {
			return "", fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
		}
		resp, err := h.uploadMultipart(ctx, "/send-file", m.JID, m.Path, m.Name, m.Caption, m.FileReplyTo, m.ThreadID)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var r struct {
					ID string `json:"id"`
				}
				json.NewDecoder(resp.Body).Decode(&r)
				return r.ID, nil
			}
			err = classifyStatus(resp.StatusCode)
		}
		return "", fmt.Errorf("channel %s send-file: %w", h.entry.Name, err)
	}
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	body := map[string]string{"chat_jid": m.JID, "content": m.Content}
	if m.ReplyTo != "" {
		body["reply_to"] = m.ReplyTo
	}
	if m.ThreadID != "" {
		body["thread_id"] = m.ThreadID
	}
	if m.ThreadRoot != "" {
		body["thread_root"] = m.ThreadRoot
	}
	if m.TurnID != "" {
		body["turn_id"] = m.TurnID
	}
	b, _ := json.Marshal(body)
	httpResp, err := h.post(ctx, "/send", b)
	if err == nil {
		defer httpResp.Body.Close()
		if httpResp.StatusCode == http.StatusOK {
			var resp struct {
				ID string `json:"id"`
			}
			json.NewDecoder(httpResp.Body).Decode(&resp)
			return resp.ID, nil
		}
		err = classifyStatus(httpResp.StatusCode)
	}
	return "", fmt.Errorf("channel %s send: %w", h.entry.Name, err)
}

func (h *HTTPChannel) SendFile(jid, path, name, caption, replyTo, threadID string) (string, error) {
	return h.SendFileCtx(context.Background(), jid, path, name, caption, replyTo, threadID)
}

func (h *HTTPChannel) SendFileCtx(ctx context.Context, jid, path, name, caption, replyTo, threadID string) (string, error) {
	if !h.entry.HasCap("send_file") {
		return "", fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
	}
	m := outMsg{JID: jid, IsFile: true, Path: path, Name: name, Caption: caption, FileReplyTo: replyTo, ThreadID: threadID}
	id, err := h.trySend(ctx, m)
	if err != nil && !errors.Is(err, errPermanent) {
		h.enqueue(m)
	}
	return id, err
}

func (h *HTTPChannel) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	return h.SendVoiceCtx(context.Background(), jid, audioPath, caption, threadID)
}

func (h *HTTPChannel) SendVoiceCtx(ctx context.Context, jid, audioPath, caption, threadID string) (string, error) {
	if !h.entry.HasCap("send_voice") {
		return "", chanlib.Unsupported("send_voice", h.entry.Name, "adapter does not advertise voice capability")
	}
	resp, err := h.uploadMultipart(ctx, "/send-voice", jid, audioPath, filepath.Base(audioPath), caption, "", threadID)
	if err != nil {
		return "", fmt.Errorf("channel %s send-voice: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "send_voice", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s send-voice: status %d", h.entry.Name, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

// uploadMultipart builds a multipart body and POSTs to endpoint.
func (h *HTTPChannel) uploadMultipart(ctx context.Context, endpoint, jid, path, name, caption, replyTo, threadID string) (*http.Response, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_jid", jid)
	w.WriteField("filename", name)
	if caption != "" {
		w.WriteField("caption", caption)
	}
	if replyTo != "" {
		w.WriteField("reply_to", replyTo)
	}
	if threadID != "" {
		w.WriteField("thread_id", threadID)
	}
	formName := name
	if formName == "" {
		formName = filepath.Base(path)
	}
	fw, err := w.CreateFormFile("file", formName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(fw, f); err != nil {
		return nil, fmt.Errorf("copy file: %w", err)
	}
	w.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", h.entry.URL+endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	h.authHeader(ctx, req)
	return h.client.Do(req)
}

func (h *HTTPChannel) Typing(jid string, on bool) error {
	if !h.entry.HasCap("typing") {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"chat_jid": jid, "on": on})
	resp, err := h.post(context.Background(), "/typing", b)
	if err != nil {
		slog.Warn("typing request failed", "channel", h.entry.Name, "jid", jid, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("typing: non-2xx response", "channel", h.entry.Name, "status", resp.StatusCode)
	}
	return nil
}

// PostRoundDone signals the channel that the agent's run for turnID has closed.
// 404 is treated as "channel doesn't implement round-handle protocol" — not an error.
func (h *HTTPChannel) PostRoundDone(folder, turnID, status, errMsg string) error {
	body := map[string]string{"folder": folder, "turn_id": turnID, "status": status}
	if errMsg != "" {
		body["error"] = errMsg
	}
	b, _ := json.Marshal(body)
	resp, err := h.post(context.Background(), "/v1/round_done", b)
	if err != nil {
		return fmt.Errorf("channel %s round_done: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s round_done: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// FetchHistory proxies to the adapter's GET /v1/history endpoint.
// Returns an error if the adapter doesn't advertise the fetch_history cap.
func (h *HTTPChannel) FetchHistory(ctx context.Context, jid string, before time.Time, limit int) ([]byte, error) {
	if !h.entry.HasCap("fetch_history") {
		return nil, fmt.Errorf("channel %s: fetch_history not supported", h.entry.Name)
	}
	u := fmt.Sprintf("%s/v1/history?jid=%s&limit=%d", h.entry.URL, jid, limit)
	if !before.IsZero() {
		u += "&before=" + before.UTC().Format(time.RFC3339)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	h.authHeader(ctx, req)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("channel %s fetch_history: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("channel %s fetch_history: status %d", h.entry.Name, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// postVerb posts JSON to endpoint, handles 501→ErrUnsupported, non-200→error,
// and decodes an optional "id" field from the response. Used by social verbs.
func (h *HTTPChannel) postVerb(ctx context.Context, verb, endpoint string, body []byte) (string, error) {
	resp, err := h.post(ctx, endpoint, body)
	if err != nil {
		return "", fmt.Errorf("channel %s %s: %w", h.entry.Name, verb, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, verb, h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s %s: status %d", h.entry.Name, verb, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

func (h *HTTPChannel) Post(ctx context.Context, jid, content string, mediaPaths []string) (string, error) {
	body := map[string]any{"chat_jid": jid, "content": content}
	if len(mediaPaths) > 0 {
		body["media_paths"] = mediaPaths
	}
	b, _ := json.Marshal(body)
	return h.postVerb(ctx, "post", "/post", b)
}

func makeBody(fields map[string]string) []byte {
	b, _ := json.Marshal(fields)
	return b
}

func (h *HTTPChannel) Like(ctx context.Context, jid, targetID, reaction string) error {
	_, err := h.postVerb(ctx, "like", "/like", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID, "reaction": reaction}))
	return err
}

func (h *HTTPChannel) Delete(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("delete") {
		return chanlib.Unsupported("delete", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "delete", "/delete", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Pin(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("pin") {
		return chanlib.Unsupported("pin", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "pin", "/pin", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Unpin(ctx context.Context, jid, targetID string, all bool) error {
	if !h.entry.HasCap("pin") {
		return chanlib.Unsupported("unpin", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]any{"chat_jid": jid}
	if all {
		body["all"] = true
	} else {
		body["target_id"] = targetID
	}
	b, _ := json.Marshal(body)
	_, err := h.postVerb(ctx, "unpin", "/unpin", b)
	return err
}

func (h *HTTPChannel) Forward(ctx context.Context, sourceMsgID, targetJID, comment string) (string, error) {
	if !h.entry.HasCap("fwd") {
		return "", chanlib.Unsupported("forward", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]string{"source_msg_id": sourceMsgID, "target_jid": targetJID}
	if comment != "" {
		body["comment"] = comment
	}
	return h.postVerb(ctx, "forward", "/forward", makeBody(body))
}

func (h *HTTPChannel) Quote(ctx context.Context, jid, sourceMsgID, comment string) (string, error) {
	if !h.entry.HasCap("quote") {
		return "", chanlib.Unsupported("quote", h.entry.Name, "adapter does not advertise capability")
	}
	return h.postVerb(ctx, "quote", "/quote", makeBody(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID, "comment": comment}))
}

func (h *HTTPChannel) Repost(ctx context.Context, jid, sourceMsgID string) (string, error) {
	if !h.entry.HasCap("repost") {
		return "", chanlib.Unsupported("repost", h.entry.Name, "adapter does not advertise capability")
	}
	return h.postVerb(ctx, "repost", "/repost", makeBody(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID}))
}

func (h *HTTPChannel) Dislike(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("dislike") {
		return chanlib.Unsupported("dislike", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "dislike", "/dislike", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID}))
	return err
}

func (h *HTTPChannel) Edit(ctx context.Context, jid, targetID, content string) error {
	if !h.entry.HasCap("edit") {
		return chanlib.Unsupported("edit", h.entry.Name, "adapter does not advertise capability")
	}
	_, err := h.postVerb(ctx, "edit", "/edit", makeBody(map[string]string{"chat_jid": jid, "target_id": targetID, "content": content}))
	return err
}

// SetSuggestions targets the adapter's /v1/pane/prompts endpoint
// (Slack-only today; spec 6/D). Adapters without the capability
// respond 404 → ErrUnsupported. Idempotent; one-shot per outbound.
func (h *HTTPChannel) SetSuggestions(ctx context.Context, jid string, prompts []core.PanePrompt) error {
	b, _ := json.Marshal(map[string]any{"jid": jid, "prompts": prompts})
	resp, err := h.post(ctx, "/v1/pane/prompts", b)
	if err != nil {
		return fmt.Errorf("channel %s pane_set_prompts: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return chanlib.Unsupported("pane_set_prompts", h.entry.Name, "adapter has no open pane for jid (or doesn't support suggestions)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s pane_set_prompts: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) SetName(ctx context.Context, jid, name string) error {
	b, _ := json.Marshal(map[string]string{"jid": jid, "title": name})
	resp, err := h.post(ctx, "/v1/pane/title", b)
	if err != nil {
		return fmt.Errorf("channel %s pane_set_title: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return chanlib.Unsupported("pane_set_title", h.entry.Name, "adapter has no open pane for jid (or doesn't support rename)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s pane_set_title: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) Disconnect() error { return nil }

func (h *HTTPChannel) HealthCheck() error { return healthPing(h.client, h.entry.URL) }

func (h *HTTPChannel) DrainOutbox() {
	h.mu.Lock()
	q := h.outbox
	h.outbox = nil
	h.mu.Unlock()

	for _, m := range q {
		// trySend does NOT re-enqueue (unlike Send), so the drain owns the retry
		// policy. CONTINUE past a failure — never let one dead jid block the rest
		// of the batch (head-of-line). Re-queue with a bounded attempt counter so a
		// permanently-dead jid (group deleted, bot kicked) self-evicts.
		if _, err := h.trySend(context.Background(), m); err != nil {
			if errors.Is(err, errPermanent) {
				slog.Warn("outbox drain: permanent error, dropping",
					"channel", h.entry.Name, "jid", m.JID, "err", err)
				continue
			}
			m.Attempts++
			if m.Attempts < maxSendAttempts {
				h.enqueue(m)
				slog.Warn("outbox drain failed, requeued",
					"channel", h.entry.Name, "jid", m.JID, "attempt", m.Attempts, "err", err)
			} else {
				slog.Warn("outbox drain failed, dropping after max attempts",
					"channel", h.entry.Name, "jid", m.JID, "attempts", m.Attempts, "err", err)
			}
		}
	}
}

func (h *HTTPChannel) QueueLen() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.outbox)
}

func (h *HTTPChannel) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", h.entry.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	h.authHeader(ctx, req)
	return h.client.Do(req)
}

func (h *HTTPChannel) enqueue(m outMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.outbox) >= maxOutbox {
		slog.Warn("outbox full, dropping message", "channel", h.entry.Name, "jid", m.JID)
		return
	}
	h.outbox = append(h.outbox, m)
}

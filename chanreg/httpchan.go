package chanreg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

// ErrUnsupported is returned when an adapter responds 501 to a social
// verb (post/like/delete) because the underlying platform doesn't
// support that action. Aliased to chanlib.ErrUnsupported so existing
// errors.Is(err, ErrUnsupported) checks chain through structured
// *chanlib.UnsupportedError values too.
var ErrUnsupported = chanlib.ErrUnsupported

// decodeUnsupported reads a 501 body into a structured *chanlib.UnsupportedError.
// On parse failure or empty body, it returns plain ErrUnsupported.
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

type HTTPChannel struct {
	entry  *Entry
	secret string
	client *http.Client

	mu     sync.RWMutex
	outbox []outMsg
}

type outMsg struct {
	JID      string
	Content  string
	ReplyTo  string
	ThreadID string
	TurnID   string
	IsFile   bool
	Path     string
	Name     string
	Caption  string
}

func NewHTTPChannel(e *Entry, secret string) *HTTPChannel {
	return &HTTPChannel{
		entry:  e,
		secret: secret,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HTTPChannel) Name() string { return h.entry.Name }

func (h *HTTPChannel) Connect(_ context.Context) error { return nil }

func (h *HTTPChannel) Owns(jid string) bool { return h.entry.Owns(jid) }

func (h *HTTPChannel) Send(jid, text, replyTo, threadID, turnID string) (string, error) {
	return h.SendCtx(context.Background(), jid, text, replyTo, threadID, turnID)
}

func (h *HTTPChannel) SendCtx(ctx context.Context, jid, text, replyTo, threadID, turnID string) (string, error) {
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	body := map[string]string{
		"chat_jid": jid,
		"content":  text,
	}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	if threadID != "" {
		body["thread_id"] = threadID
	}
	if turnID != "" {
		body["turn_id"] = turnID
	}
	b, _ := json.Marshal(body)

	httpResp, err := h.post(ctx, "/send", b)
	if err == nil {
		defer httpResp.Body.Close()
		if httpResp.StatusCode == http.StatusOK {
			var resp struct {
				ID string `json:"id"`
			}
			json.NewDecoder(httpResp.Body).Decode(&resp) // ignore decode error — id may be absent
			return resp.ID, nil
		}
		err = fmt.Errorf("status %d", httpResp.StatusCode)
	}
	h.enqueue(outMsg{JID: jid, Content: text, ReplyTo: replyTo, ThreadID: threadID, TurnID: turnID})
	return "", fmt.Errorf("channel %s send: %w", h.entry.Name, err)
}

func (h *HTTPChannel) SendFile(jid, path, name, caption string) error {
	return h.SendFileCtx(context.Background(), jid, path, name, caption)
}

func (h *HTTPChannel) SendFileCtx(ctx context.Context, jid, path, name, caption string) error {
	if !h.entry.HasCap("send_file") {
		return fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_jid", jid)
	w.WriteField("filename", name)
	if caption != "" {
		w.WriteField("caption", caption)
	}

	formName := name
	if formName == "" {
		formName = filepath.Base(path)
	}
	fw, err := w.CreateFormFile("file", formName)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(fw, f); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	w.Close()

	url := h.entry.URL + "/send-file"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+h.secret)

	resp, err := h.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		err = fmt.Errorf("status %d", resp.StatusCode)
	}
	h.enqueue(outMsg{JID: jid, IsFile: true, Path: path, Name: name, Caption: caption})
	return fmt.Errorf("channel %s send-file: %w", h.entry.Name, err)
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

// PostRoundDone signals the channel that the agent's run for turnID has
// closed. Used by webd to emit a terminal round_done SSE frame on slink
// round-handle subscriptions. 404 from the channel is treated as
// "channel doesn't implement round-handle protocol" — not an error.
func (h *HTTPChannel) PostRoundDone(folder, turnID, status, errMsg string) error {
	body := map[string]string{
		"folder":  folder,
		"turn_id": turnID,
		"status":  status,
	}
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

// FetchHistory proxies to the adapter's GET /v1/history endpoint and
// returns the raw JSON bytes. The caller decodes into chanlib.HistoryResponse.
// Returns an error if the adapter doesn't advertise the fetch_history cap or
// if the request fails.
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
	req.Header.Set("Authorization", "Bearer "+h.secret)
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

// Post publishes a standalone post on the adapter (feed/timeline/subreddit).
// Returns the platform-native ID. Returns ErrUnsupported if the adapter
// returns 501 (i.e. the platform doesn't support posts).
func (h *HTTPChannel) Post(ctx context.Context, jid, content string, mediaPaths []string) (string, error) {
	body := map[string]any{"chat_jid": jid, "content": content}
	if len(mediaPaths) > 0 {
		body["media_paths"] = mediaPaths
	}
	b, _ := json.Marshal(body)
	resp, err := h.post(ctx, "/post", b)
	if err != nil {
		return "", fmt.Errorf("channel %s post: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "post", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s post: status %d", h.entry.Name, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

// Like attaches a like/favourite/emoji reaction to a post or message.
func (h *HTTPChannel) Like(ctx context.Context, jid, targetID, reaction string) error {
	b, _ := json.Marshal(map[string]string{
		"chat_jid":  jid,
		"target_id": targetID,
		"reaction":  reaction,
	})
	resp, err := h.post(ctx, "/like", b)
	if err != nil {
		return fmt.Errorf("channel %s like: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return decodeUnsupported(resp.Body, "like", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s like: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// Delete removes a post or message authored by the bot.
func (h *HTTPChannel) Delete(ctx context.Context, jid, targetID string) error {
	b, _ := json.Marshal(map[string]string{
		"chat_jid":  jid,
		"target_id": targetID,
	})
	resp, err := h.post(ctx, "/delete", b)
	if err != nil {
		return fmt.Errorf("channel %s delete: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return decodeUnsupported(resp.Body, "delete", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s delete: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// Forward redelivers an existing message to a different chat (Telegram
// forwardMessage, WhatsApp forward, email Fwd:). Returns ErrUnsupported
// when the adapter doesn't advertise the `fwd` capability.
func (h *HTTPChannel) Forward(ctx context.Context, sourceMsgID, targetJID, comment string) (string, error) {
	if !h.entry.HasCap("fwd") {
		return "", chanlib.Unsupported("forward", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]string{"source_msg_id": sourceMsgID, "target_jid": targetJID}
	if comment != "" {
		body["comment"] = comment
	}
	b, _ := json.Marshal(body)
	resp, err := h.post(ctx, "/forward", b)
	if err != nil {
		return "", fmt.Errorf("channel %s forward: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "forward", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s forward: status %d", h.entry.Name, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

// Quote republishes a message on the bot's feed with added commentary.
func (h *HTTPChannel) Quote(ctx context.Context, jid, sourceMsgID, comment string) (string, error) {
	if !h.entry.HasCap("quote") {
		return "", chanlib.Unsupported("quote", h.entry.Name, "adapter does not advertise capability")
	}
	body := map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID, "comment": comment}
	b, _ := json.Marshal(body)
	resp, err := h.post(ctx, "/quote", b)
	if err != nil {
		return "", fmt.Errorf("channel %s quote: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "quote", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s quote: status %d", h.entry.Name, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

// Repost amplifies a message on the bot's feed without commentary.
func (h *HTTPChannel) Repost(ctx context.Context, jid, sourceMsgID string) (string, error) {
	if !h.entry.HasCap("repost") {
		return "", chanlib.Unsupported("repost", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID})
	resp, err := h.post(ctx, "/repost", b)
	if err != nil {
		return "", fmt.Errorf("channel %s repost: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return "", decodeUnsupported(resp.Body, "repost", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel %s repost: status %d", h.entry.Name, resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.ID, nil
}

// Dislike attaches a downvote/negative reaction to a message.
func (h *HTTPChannel) Dislike(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("dislike") {
		return chanlib.Unsupported("dislike", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID})
	resp, err := h.post(ctx, "/dislike", b)
	if err != nil {
		return fmt.Errorf("channel %s dislike: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return decodeUnsupported(resp.Body, "dislike", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s dislike: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// Edit modifies a previously-sent bot message in place.
func (h *HTTPChannel) Edit(ctx context.Context, jid, targetID, content string) error {
	if !h.entry.HasCap("edit") {
		return chanlib.Unsupported("edit", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID, "content": content})
	resp, err := h.post(ctx, "/edit", b)
	if err != nil {
		return fmt.Errorf("channel %s edit: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return decodeUnsupported(resp.Body, "edit", h.entry.Name)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s edit: status %d", h.entry.Name, resp.StatusCode)
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
		var err error
		if m.IsFile {
			err = h.SendFile(m.JID, m.Path, m.Name, m.Caption)
		} else {
			_, err = h.Send(m.JID, m.Content, m.ReplyTo, m.ThreadID, m.TurnID)
		}
		if err != nil {
			slog.Warn("outbox drain failed", "channel", h.entry.Name,
				"jid", m.JID, "err", err)
			return // stop draining on first failure
		}
	}
}

func (h *HTTPChannel) QueueLen() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.outbox)
}

func (h *HTTPChannel) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := h.entry.URL + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.secret)
	return h.client.Do(req)
}

func (h *HTTPChannel) enqueue(m outMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.outbox) >= maxOutbox {
		slog.Warn("outbox full, dropping message",
			"channel", h.entry.Name, "jid", m.JID)
		return
	}
	h.outbox = append(h.outbox, m)
}

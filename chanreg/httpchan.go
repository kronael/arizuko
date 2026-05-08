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
	body := map[string]string{"chat_jid": jid, "content": text}
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
			json.NewDecoder(httpResp.Body).Decode(&resp)
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
	resp, err := h.uploadMultipart(ctx, "/send-file", jid, path, name, caption)
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

func (h *HTTPChannel) SendVoice(jid, audioPath, caption string) (string, error) {
	return h.SendVoiceCtx(context.Background(), jid, audioPath, caption)
}

func (h *HTTPChannel) SendVoiceCtx(ctx context.Context, jid, audioPath, caption string) (string, error) {
	if !h.entry.HasCap("send_voice") {
		return "", chanlib.Unsupported("send_voice", h.entry.Name, "adapter does not advertise voice capability")
	}
	resp, err := h.uploadMultipart(ctx, "/send-voice", jid, audioPath, filepath.Base(audioPath), caption)
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
func (h *HTTPChannel) uploadMultipart(ctx context.Context, endpoint, jid, path, name, caption string) (*http.Response, error) {
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
	req.Header.Set("Authorization", "Bearer "+h.secret)
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

func (h *HTTPChannel) Like(ctx context.Context, jid, targetID, reaction string) error {
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID, "reaction": reaction})
	_, err := h.postVerb(ctx, "like", "/like", b)
	return err
}

func (h *HTTPChannel) Delete(ctx context.Context, jid, targetID string) error {
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID})
	_, err := h.postVerb(ctx, "delete", "/delete", b)
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
	b, _ := json.Marshal(body)
	return h.postVerb(ctx, "forward", "/forward", b)
}

func (h *HTTPChannel) Quote(ctx context.Context, jid, sourceMsgID, comment string) (string, error) {
	if !h.entry.HasCap("quote") {
		return "", chanlib.Unsupported("quote", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID, "comment": comment})
	return h.postVerb(ctx, "quote", "/quote", b)
}

func (h *HTTPChannel) Repost(ctx context.Context, jid, sourceMsgID string) (string, error) {
	if !h.entry.HasCap("repost") {
		return "", chanlib.Unsupported("repost", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "source_msg_id": sourceMsgID})
	return h.postVerb(ctx, "repost", "/repost", b)
}

func (h *HTTPChannel) Dislike(ctx context.Context, jid, targetID string) error {
	if !h.entry.HasCap("dislike") {
		return chanlib.Unsupported("dislike", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID})
	_, err := h.postVerb(ctx, "dislike", "/dislike", b)
	return err
}

func (h *HTTPChannel) Edit(ctx context.Context, jid, targetID, content string) error {
	if !h.entry.HasCap("edit") {
		return chanlib.Unsupported("edit", h.entry.Name, "adapter does not advertise capability")
	}
	b, _ := json.Marshal(map[string]string{"chat_jid": jid, "target_id": targetID, "content": content})
	_, err := h.postVerb(ctx, "edit", "/edit", b)
	return err
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
			slog.Warn("outbox drain failed", "channel", h.entry.Name, "jid", m.JID, "err", err)
			return
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
	req.Header.Set("Authorization", "Bearer "+h.secret)
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

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
)

// ErrUnsupported is returned when an adapter responds 501 to a social
// verb (post/like/delete-post) because the underlying platform doesn't
// support that action.
var ErrUnsupported = errors.New("unsupported")

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

func (h *HTTPChannel) Send(jid, text, replyTo, threadID string) (string, error) {
	return h.SendCtx(context.Background(), jid, text, replyTo, threadID)
}

func (h *HTTPChannel) SendCtx(ctx context.Context, jid, text, replyTo, threadID string) (string, error) {
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
	h.enqueue(outMsg{JID: jid, Content: text, ReplyTo: replyTo, ThreadID: threadID})
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
		return "", ErrUnsupported
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
		return ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s like: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

// DeletePost removes a post or message authored by the bot.
func (h *HTTPChannel) DeletePost(ctx context.Context, jid, targetID string) error {
	b, _ := json.Marshal(map[string]string{
		"chat_jid":  jid,
		"target_id": targetID,
	})
	resp, err := h.post(ctx, "/delete-post", b)
	if err != nil {
		return fmt.Errorf("channel %s delete-post: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("channel %s delete-post: status %d", h.entry.Name, resp.StatusCode)
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
			_, err = h.Send(m.JID, m.Content, m.ReplyTo, m.ThreadID)
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

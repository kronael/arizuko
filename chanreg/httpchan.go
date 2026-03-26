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
	"strings"
	"sync"
	"time"
)

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

func (h *HTTPChannel) Owns(jid string) bool {
	for _, p := range h.entry.JIDPrefixes {
		if strings.HasPrefix(jid, p) {
			return true
		}
	}
	return false
}

func (h *HTTPChannel) Send(jid, text, replyTo, threadID string) (string, error) {
	if !h.entry.HasCap("send_text") {
		return "", fmt.Errorf("channel %s: send_text not supported", h.entry.Name)
	}
	body := map[string]string{
		"chat_jid": jid,
		"content":  text,
		"format":   "markdown",
	}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	if threadID != "" {
		body["thread_id"] = threadID
	}
	b, _ := json.Marshal(body)

	httpResp, err := h.post("/send", b)
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

func (h *HTTPChannel) SendFile(jid, path, name string) error {
	if !h.entry.HasCap("send_file") {
		return fmt.Errorf("channel %s: send_file not supported", h.entry.Name)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_jid", jid)
	w.WriteField("filename", name)

	fw, err := w.CreateFormFile("file", filepath.Base(path))
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
	req, err := http.NewRequest("POST", url, &buf)
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
	h.enqueue(outMsg{JID: jid, IsFile: true, Path: path, Name: name})
	return fmt.Errorf("channel %s send-file: %w", h.entry.Name, err)
}

func (h *HTTPChannel) Typing(jid string, on bool) error {
	if !h.entry.HasCap("typing") {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"chat_jid": jid, "on": on})
	if resp, err := h.post("/typing", b); err == nil {
		resp.Body.Close()
	}
	return nil // fire-and-forget
}

func (h *HTTPChannel) Disconnect() error { return nil }

func (h *HTTPChannel) HealthCheck() error {
	resp, err := h.client.Get(h.entry.URL + "/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: status %d", resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) DrainOutbox() {
	h.mu.Lock()
	q := h.outbox
	h.outbox = nil
	h.mu.Unlock()

	for _, m := range q {
		var err error
		if m.IsFile {
			err = h.SendFile(m.JID, m.Path, m.Name)
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

func (h *HTTPChannel) post(path string, body []byte) (*http.Response, error) {
	url := h.entry.URL + path
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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

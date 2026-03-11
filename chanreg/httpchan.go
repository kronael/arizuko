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

type HTTPChannel struct {
	entry  *Entry
	secret string
	client *http.Client

	mu      sync.RWMutex
	outbox  []outMsg
	maxQueue int
}

type outMsg struct {
	JID     string
	Content string
	IsFile  bool
	Path    string
	Name    string
}

func NewHTTPChannel(e *Entry, secret string) *HTTPChannel {
	return &HTTPChannel{
		entry:  e,
		secret: secret,
		client: &http.Client{Timeout: 30 * time.Second},
		maxQueue: 1000,
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

func (h *HTTPChannel) Send(jid, text string) error {
	if !h.entry.HasCap("send_text") {
		return nil
	}
	body := map[string]string{
		"chat_jid": jid,
		"content":  text,
		"format":   "markdown",
	}
	b, _ := json.Marshal(body)

	resp, err := h.post("/send", b)
	if err != nil {
		h.enqueue(outMsg{JID: jid, Content: text})
		return fmt.Errorf("channel %s send: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.enqueue(outMsg{JID: jid, Content: text})
		return fmt.Errorf("channel %s send: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) SendFile(jid, path, name string) error {
	if !h.entry.HasCap("send_file") {
		return nil
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
	if err != nil {
		h.enqueue(outMsg{JID: jid, IsFile: true, Path: path, Name: name})
		return fmt.Errorf("channel %s send-file: %w", h.entry.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.enqueue(outMsg{JID: jid, IsFile: true, Path: path, Name: name})
		return fmt.Errorf("channel %s send-file: status %d", h.entry.Name, resp.StatusCode)
	}
	return nil
}

func (h *HTTPChannel) Typing(jid string, on bool) error {
	if !h.entry.HasCap("typing") {
		return nil
	}
	body := map[string]any{"chat_jid": jid, "on": on}
	b, _ := json.Marshal(body)
	resp, err := h.post("/typing", b)
	if err != nil {
		return nil // typing is fire-and-forget
	}
	resp.Body.Close()
	return nil
}

func (h *HTTPChannel) Disconnect() error { return nil }

func (h *HTTPChannel) HealthCheck() error {
	url := h.entry.URL + "/health"
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
			err = h.Send(m.JID, m.Content)
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
	if len(h.outbox) >= h.maxQueue {
		slog.Warn("outbox full, dropping message",
			"channel", h.entry.Name, "jid", m.JID)
		return
	}
	h.outbox = append(h.outbox, m)
}

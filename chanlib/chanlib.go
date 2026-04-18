// Package chanlib provides shared primitives for channel adapter daemons.
package chanlib

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxRouterResponseBytes caps decoded router responses to guard
// adapters from OOM on a malicious or buggy router.
const maxRouterResponseBytes = 10 << 20

// UserAgent is the User-Agent value adapter HTTP clients send on
// outbound requests. Mastodon and Bluesky recommend a custom UA for
// rate-limit attribution; Reddit requires one.
const UserAgent = "arizuko/0.29.0 (+https://github.com/onvos/arizuko)"

// MaxAdapterJSONBody caps JSON request bodies for adapter /send, /typing
// endpoints. Large enough for replies with embedded context, small
// enough to refuse OOM-style bodies from a compromised router.
const MaxAdapterJSONBody = 1 << 20

type InboundAttachment struct {
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Data     string `json:"data,omitempty"` // base64 inline content (whapd)
}

type InboundMsg struct {
	ID            string              `json:"id"`
	ChatJID       string              `json:"chat_jid"`
	Sender        string              `json:"sender"`
	SenderName    string              `json:"sender_name"`
	Content       string              `json:"content"`
	Timestamp     int64               `json:"timestamp"`
	Topic         string              `json:"topic,omitempty"`
	Verb          string              `json:"verb,omitempty"`
	ReplyTo       string              `json:"reply_to,omitempty"`
	ReplyToText   string              `json:"reply_to_text,omitempty"`
	ReplyToSender string              `json:"reply_to_sender,omitempty"`
	Attachments   []InboundAttachment `json:"attachments,omitempty"`
}

type RouterClient struct {
	url, secret, token string
	client             *http.Client
}

func NewRouterClient(url, secret string) *RouterClient {
	return &RouterClient{url: url, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
}

// SetToken overrides the auth token (for tests that skip Register).
func (r *RouterClient) SetToken(t string) { r.token = t }

func (r *RouterClient) Register(name, url string, prefixes []string, caps map[string]bool) (string, error) {
	slog.Info("registering channel", "name", name, "url", url)
	var resp struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Error string `json:"error"`
	}
	err := r.Post("/v1/channels/register", map[string]any{
		"name": name, "url": url, "jid_prefixes": prefixes, "capabilities": caps,
	}, r.secret, &resp)
	if err != nil {
		slog.Error("channel registration failed", "name", name, "err", err)
		return "", err
	}
	if !resp.OK {
		slog.Error("channel registration rejected", "name", name, "reason", resp.Error)
		return "", fmt.Errorf("register: %s", resp.Error)
	}
	r.token = resp.Token
	slog.Info("channel registered", "name", name)
	return resp.Token, nil
}

func (r *RouterClient) Deregister() error {
	var resp struct{ OK bool }
	return r.Post("/v1/channels/deregister", nil, r.token, &resp)
}

func (r *RouterClient) SendMessage(msg InboundMsg) error {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			slog.Warn("send retry", "err", last)
			time.Sleep(500 * time.Millisecond)
		}
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := r.Post("/v1/messages", msg, r.token, &resp); err != nil {
			last = err
			continue
		}
		if resp.OK {
			return nil
		}
		last = fmt.Errorf("deliver: %s", resp.Error)
	}
	return last
}

func (r *RouterClient) Post(path string, body any, auth string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest("POST", r.url+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("router %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("router %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxRouterResponseBytes)).Decode(out)
}

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// Auth validates the Bearer token against secret; empty secret passes all.
func Auth(secret string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if secret != "" {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
				WriteErr(w, 401, "invalid secret")
				return
			}
		}
		next(w, r)
	}
}

func Chunk(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	var cur []byte
	for _, r := range s {
		rb := []byte(string(r))
		if len(cur)+len(rb) > max {
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
		}
		cur = append(cur, rb...)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func EnvOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

func MustEnv(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	slog.Error("required env var missing", "key", k)
	os.Exit(1)
	return ""
}

// EnvBytes parses a positive int64 from env var k, returning def on
// missing/invalid/non-positive values.
func EnvBytes(k string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ProxyFile streams src → w, copying Content-Type and capping bytes at max
// (falls back to 20 MiB when max <= 0). Writes 502 on non-200 upstream.
// Caller owns closing src.Body.
func ProxyFile(w http.ResponseWriter, src *http.Response, max int64) {
	if src.StatusCode != 200 {
		WriteErr(w, 502, "upstream fetch failed")
		return
	}
	if max <= 0 {
		max = 20 * 1024 * 1024
	}
	if ct := src.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	io.Copy(w, io.LimitReader(src.Body, max))
}

// Package chanlib provides shared primitives for channel adapter daemons.
package chanlib

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRouterResponseBytes caps decoded router responses to guard against OOM.
const maxRouterResponseBytes = 10 << 20

// UserAgent is sent on outbound requests. Reddit requires one; Mastodon and
// Bluesky use it for rate-limit attribution.
const UserAgent = "arizuko/0.29.0 (+https://github.com/onvos/arizuko)"

// MaxAdapterJSONBody caps inbound JSON bodies at adapters.
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
	// Reaction carries the source emoji when Verb is "like" or "dislike"
	// triggered by an inbound reaction event. Empty for content messages.
	Reaction string `json:"reaction,omitempty"`
	// IsGroup classifies the chat: true for group chats / channels /
	// public threads, false for DMs / 1:1 threads / slink. The router
	// upserts this onto chats.is_group; the secrets resolver reads it
	// to decide whether user-scope secrets are safe to inject. Adapters
	// set this on every inbound — the latest classification wins.
	IsGroup bool `json:"is_group,omitempty"`
	// ChatName is the human-readable name of the source channel or group
	// (e.g. "#general" on Discord, "My Group" on Telegram). Empty for DMs.
	// Stored on the message so the agent can identify the channel without
	// a separate tool call.
	ChatName string `json:"chat_name,omitempty"`
}

type RouterClient struct {
	url, secret, token string
	client             *http.Client

	// Saved for transparent re-register on 401 (router dropped channel while adapter was down).
	regName     string
	regURL      string
	regPrefixes []string
	regCaps     map[string]bool
	regMu       sync.Mutex
}

func NewRouterClient(url, secret string) *RouterClient {
	return &RouterClient{url: url, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
}

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
	r.regMu.Lock()
	r.token = resp.Token
	r.regName = name
	r.regURL = url
	r.regPrefixes = prefixes
	r.regCaps = caps
	r.regMu.Unlock()
	slog.Info("channel registered", "name", name)
	return resp.Token, nil
}

func (r *RouterClient) reregister() error {
	r.regMu.Lock()
	name, url, prefixes, caps := r.regName, r.regURL, r.regPrefixes, r.regCaps
	r.regMu.Unlock()
	if name == "" {
		return fmt.Errorf("re-register: no saved params (Register was never called)")
	}
	slog.Warn("re-registering channel after 401", "name", name)
	_, err := r.Register(name, url, prefixes, caps)
	return err
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
			// 401 = router has no record of our token; re-register before retry.
			// On any other error, just back off and try again.
			if isAuthErr(last) {
				if rerr := r.reregister(); rerr != nil {
					slog.Error("re-register failed", "err", rerr)
					return last
				}
			} else {
				time.Sleep(500 * time.Millisecond)
			}
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

func isAuthErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 401")
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

func Auth(secret string, next http.HandlerFunc) http.HandlerFunc {
	if secret == "" {
		return next
	}
	secretBytes := []byte(secret)
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), secretBytes) != 1 {
			WriteErr(w, 401, "invalid secret")
			return
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

func EnvInt(k string, fallback int) int {
	s := os.Getenv(k)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func EnvBool(k string, fallback bool) bool {
	s := os.Getenv(k)
	if s == "" {
		return fallback
	}
	return s == "true"
}

// EnvDur parses os.Getenv(k) as integer milliseconds (legacy encoding
// for CONTAINER_TIMEOUT, IDLE_TIMEOUT, etc.).
func EnvDur(k string, fallback time.Duration) time.Duration {
	s := os.Getenv(k)
	if s == "" {
		return fallback
	}
	ms, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

// ShortHash returns 4-byte hex of sha256(s) for logging sensitive strings.
// Empty input returns "" so callers can skip the log field.
func ShortHash(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func MustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("required env var missing", "key", k)
		os.Exit(1)
	}
	return v
}

func EnvBytes(k string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ProxyFile streams src to w, copying Content-Type, capped at max bytes
// (defaults to 20 MiB). Caller owns src.Body.
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

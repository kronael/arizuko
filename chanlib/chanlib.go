// Package chanlib provides shared primitives for channel adapter daemons.
package chanlib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// InboundMsg is the payload delivered to the router for each incoming message.
type InboundMsg struct {
	ID         string `json:"id"`
	ChatJID    string `json:"chat_jid"`
	Sender     string `json:"sender"`
	SenderName string `json:"sender_name"`
	Content    string `json:"content"`
	Timestamp  int64  `json:"timestamp"`
	IsGroup    bool   `json:"is_group"`
}

// RouterClient posts messages and manages channel registration with the router.
type RouterClient struct {
	url, secret, Token string
	client             *http.Client
}

func NewRouterClient(url, secret string) *RouterClient {
	return &RouterClient{url: url, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *RouterClient) Register(name, url string, prefixes []string, caps map[string]bool) (string, error) {
	var resp struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Error string `json:"error"`
	}
	err := r.Post("/v1/channels/register", map[string]any{
		"name": name, "url": url, "jid_prefixes": prefixes, "capabilities": caps,
	}, r.secret, &resp)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("register: %s", resp.Error)
	}
	return resp.Token, nil
}

func (r *RouterClient) Deregister() error {
	var resp struct{ OK bool }
	return r.Post("/v1/channels/deregister", nil, r.Token, &resp)
}

func (r *RouterClient) SendMessage(msg InboundMsg) error {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	var err error
	for attempt := range 2 {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		err = r.Post("/v1/messages", msg, r.Token, &resp)
		if err == nil && resp.OK {
			return nil
		}
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("deliver: %s", resp.Error)
}

func (r *RouterClient) SendChat(jid, name string, isGroup bool) error {
	var resp struct{ OK bool }
	return r.Post("/v1/chats", map[string]any{
		"chat_jid": jid, "name": name, "is_group": isGroup,
	}, r.Token, &resp)
}

func (r *RouterClient) Post(path string, body any, auth string, out any) error {
	b, _ := json.Marshal(body)
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
	return json.NewDecoder(resp.Body).Decode(out)
}

// WriteJSON writes a 200 JSON response.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// WriteErr writes a JSON error response.
func WriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// Auth returns a middleware that validates the Bearer token against secret.
// If secret is empty, all requests pass through.
func Auth(secret string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if secret != "" {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if tok != secret {
				WriteErr(w, 401, "invalid secret")
				return
			}
		}
		next(w, r)
	}
}

// EnvOr returns the env var k or fallback v.
func EnvOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

// MustEnv returns the env var k or exits with error.
func MustEnv(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	slog.Error("required env var missing", "key", k)
	os.Exit(1)
	return ""
}

package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kronael/arizuko/anteval/pkg/check"
)

// HTTPTarget is the live adapter: it reaches a real arizuko instance through
// its REST contract only (no arizuko package imported). Paths track routd's
// `/v1` surface and proxyd's public URLs; this file is the single wiring seam
// to adjust if the surface moves.
type HTTPTarget struct {
	API    string // routd REST base reachable from the eval host (serves /v1/*)
	Token  string // bearer token; needs messages:write + read scope on the eval folder
	MCPURL string // base of an inspect-compatible MCP-over-HTTP face; empty disables mcp checks
	Client *http.Client
}

func (t *HTTPTarget) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (t *HTTPTarget) do(method, url string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return nil, err
	}
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return t.client().Do(req)
}

// Inject posts the task as an inbound message (routd POST /v1/messages) and
// returns the stored id. routd mints/echoes the id and routes the message to
// the chat's agent.
func (t *HTTPTarget) Inject(chat, prompt string) (string, error) {
	resp, err := t.do(http.MethodPost, t.API+"/v1/messages",
		map[string]any{"chat_jid": chat, "content": prompt, "sender": "user:anteval"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("inject %s: %d", chat, resp.StatusCode)
	}
	var ack struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&ack)
	return ack.ID, nil
}

func (t *HTTPTarget) messages(base, chat string) ([]check.Msg, error) {
	if base == "" {
		return nil, fmt.Errorf("surface not configured")
	}
	resp, err := t.do(http.MethodGet, base+"/v1/messages/inspect?jid="+url.QueryEscape(chat), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("read %s: %d", chat, resp.StatusCode)
	}
	var out struct {
		Messages []struct {
			Content  string `json:"content"`
			IsBotMsg bool   `json:"is_bot_message"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	msgs := make([]check.Msg, len(out.Messages))
	for i, m := range out.Messages {
		msgs[i] = check.Msg{FromBot: m.IsBotMsg, Text: m.Content}
	}
	return msgs, nil
}

// RestMessages reads a chat via routd REST (GET /v1/messages/inspect).
func (t *HTTPTarget) RestMessages(chat string) ([]check.Msg, error) { return t.messages(t.API, chat) }

// McpMessages reads the same chat via an inspect-compatible MCP-over-HTTP face
// (--mcp). Empty MCPURL → "surface not configured", so mcp/parity cases fail
// loudly rather than false-pass.
func (t *HTTPTarget) McpMessages(chat string) ([]check.Msg, error) { return t.messages(t.MCPURL, chat) }

// Cost reports the turn's token spend. routd exposes no cost READ endpoint
// (cost is write-only via POST /v1/cost), so this is 0 over pure REST; token
// budgets are enforced only where a cost source is wired.
func (t *HTTPTarget) Cost(string) (int, error) { return 0, nil }

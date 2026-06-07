package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HTTPTarget is the live adapter: it reaches a real arizuko instance through
// its public surfaces only. Paths are the documented routd/proxyd REST shape;
// they are the single wiring seam to adjust if the surface moves. No arizuko
// package is imported — this is a black-box client.
type HTTPTarget struct {
	Base   string // proxyd base, e.g. https://krons.fiu.wtf
	API    string // routd REST base, e.g. https://krons.fiu.wtf/api
	Token  string // bearer token scoped to the eval root folder
	MCPURL string // MCP-over-HTTP endpoint (5/5 uniform face); empty disables MCP checks
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

// Inject posts the task into a chat via routd REST and returns the turn id.
func (t *HTTPTarget) Inject(_, chat, prompt string) (string, error) {
	resp, err := t.do(http.MethodPost, t.API+"/chats/"+url.PathEscape(chat)+"/messages",
		map[string]string{"content": prompt})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("inject %s: %d", chat, resp.StatusCode)
	}
	var out struct {
		TurnID string `json:"turn_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.TurnID, nil
}

func (t *HTTPTarget) messages(base, chat string) ([]string, error) {
	if base == "" {
		return nil, fmt.Errorf("surface not configured")
	}
	resp, err := t.do(http.MethodGet, base+"/chats/"+url.PathEscape(chat)+"/messages", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("read %s: %d", chat, resp.StatusCode)
	}
	var rows []struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Content
	}
	return out, nil
}

// RestMessages reads a chat's bodies via routd REST.
func (t *HTTPTarget) RestMessages(chat string) ([]string, error) { return t.messages(t.API, chat) }

// McpMessages reads the same chat via the MCP-over-HTTP face (5/5).
func (t *HTTPTarget) McpMessages(chat string) ([]string, error) { return t.messages(t.MCPURL, chat) }

// Cost returns the token spend for a turn via routd REST; best-effort.
func (t *HTTPTarget) Cost(turnID string) (int, error) {
	if turnID == "" {
		return 0, nil
	}
	resp, err := t.do(http.MethodGet, t.API+"/turns/"+url.PathEscape(turnID)+"/cost", nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	var out struct {
		Tokens int `json:"tokens"`
	}
	if json.Unmarshal(b, &out) == nil && out.Tokens > 0 {
		return out.Tokens, nil
	}
	n, _ := strconv.Atoi(string(bytes.TrimSpace(b)))
	return n, nil
}

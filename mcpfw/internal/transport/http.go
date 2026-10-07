// Package transport carries JSON-RPC over HTTP. MCP connectors are HTTP with a
// url and headers, never stdio, so that is the one transport here.
package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPUpstream posts raw JSON-RPC to an MCP server.
type HTTPUpstream struct {
	URL     string
	Headers map[string]string
	Client  *http.Client
}

// New returns an upstream with a bounded client. A model call can be slow, so
// the ceiling is generous, but absent it a hung upstream pins the gate open.
func New(url string, headers map[string]string) *HTTPUpstream {
	return &HTTPUpstream{URL: url, Headers: headers,
		Client: &http.Client{Timeout: 120 * time.Second}}
}

func (u *HTTPUpstream) Call(raw json.RawMessage) (json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodPost, u.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mcpfw: build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range u.Headers {
		req.Header.Set(k, v)
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcpfw: upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("mcpfw: read upstream: %w", err)
	}
	return body, nil
}

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a thin HTTP client for runed's /v1/* surface. routd holds one
// to call POST /v1/runs. The bearer is a service token (routd's own
// service:routd token); the caller sets the static Token, or a refreshing
// TokenFn (auth.ServiceToken) for the boot-exchange cutover path.
type Client struct {
	BaseURL string
	Token   string                                // static fallback bearer
	TokenFn func(context.Context) (string, error) // live service token; wins when non-nil
	HTTP    *http.Client
}

// NewClient builds a Client against baseURL with a default HTTP client.
// timeout bounds a single run call (RUNED_RUN_TIMEOUT). Pass 0 for the
// stdlib default (no client-side deadline; rely on the request context).
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// NewClientWithSource is NewClient with a refreshing token source
// (auth.ServiceToken) instead of a static bearer — the HMAC→ES256 cutover
// path. tokenFn is consulted per call; the TokenSource caches + refreshes.
func NewClientWithSource(baseURL string, tokenFn func(context.Context) (string, error), timeout time.Duration) *Client {
	return &Client{
		BaseURL: baseURL,
		TokenFn: tokenFn,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// bearer returns the live token (TokenFn) when configured, else the static one.
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.TokenFn != nil {
		return c.TokenFn(ctx)
	}
	return c.Token, nil
}

// Run posts a RunRequest to POST /v1/runs and blocks until the run
// completes (the turn boundary). A non-2xx with a decodable Err body is
// returned as an *APIError so the caller can distinguish a clean
// outcome:error (200 body) from a transport failure (this error).
func (c *Client) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	var out RunOutcome
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/runs", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e Err
		_ = json.Unmarshal(raw, &e)
		return out, &APIError{Status: resp.StatusCode, Code: e.Error, Msg: e.Message}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode run outcome: %w", err)
	}
	return out, nil
}

// StopFolder posts to POST /v1/runs/stop — the operator-kill path (routd's
// /stop). runed maps the folder to its live spawn and kills it, returning
// whether something was killed. A transport failure surfaces as the bare error.
func (c *Client) StopFolder(ctx context.Context, folder string) (StopRunResponse, error) {
	var out StopRunResponse
	body, err := json.Marshal(StopRunRequest{Folder: folder})
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/runs/stop", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e Err
		_ = json.Unmarshal(raw, &e)
		return out, &APIError{Status: resp.StatusCode, Code: e.Error, Msg: e.Message}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode stop response: %w", err)
	}
	return out, nil
}

// APIError is a non-2xx response from runed carrying the decoded Err
// envelope. A transport failure (no HTTP response) surfaces as the bare
// network error instead — the distinction routd keys on for cursor
// advance (spec § Transport failure vs outcome:error).
type APIError struct {
	Status int
	Code   string
	Msg    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("runed %d %s: %s", e.Status, e.Code, e.Msg)
}

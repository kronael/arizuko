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
// service:routd token); the caller sets it via Token.
type Client struct {
	BaseURL string
	Token   string
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
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
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

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/types"
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

// call is the one request shape every method below shares: marshal → request →
// bearer → headers → Do → read → non-200 becomes an *APIError → decode. It was
// written out five times; the differences were exactly the four parameters
// here, so five copies could drift on the error envelope that routd keys on for
// cursor advance.
//
// `trace` is not cosmetic: only Run and Hold inject the trace context, because
// only those two open a span the callee continues. `decodeAs` names the decode
// error the caller sees; empty means the response carries no body to decode.
func call[T any](ctx context.Context, c *Client, method, path string, body any, trace bool, decodeAs string) (T, error) {
	var out T
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return out, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return out, err
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return out, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if trace {
		obs.InjectRequest(ctx, req)
	}
	resp, err := c.HTTP.Do(req)
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
	if decodeAs == "" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode %s: %w", decodeAs, err)
	}
	return out, nil
}

// Run posts a RunRequest to POST /v1/runs and blocks until the run
// completes (the turn boundary). A non-2xx with a decodable Err body is
// returned as an *APIError so the caller can distinguish a clean
// outcome:error (200 body) from a transport failure (this error).
func (c *Client) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	return call[RunOutcome](ctx, c, http.MethodPost, "/v1/runs", req, true, "run outcome")
}

// StopFolder posts to POST /v1/runs/stop — the operator-kill path (routd's
// /stop). runed maps the folder to its live spawn and kills it, returning
// whether something was killed. A transport failure surfaces as the bare error.
func (c *Client) StopFolder(ctx context.Context, folder string) (StopRunResponse, error) {
	return call[StopRunResponse](ctx, c, http.MethodPost, "/v1/runs/stop",
		StopRunRequest{Folder: folder}, false, "stop response")
}

// Hold posts to POST /v1/holds — claim folder's run slot for a
// folder-exclusive external job — and returns as soon as the slot is claimed
// (NOT at a turn boundary). Release the returned RunID with ReleaseHold.
// out.Busy=true means the folder had a live run and nothing was claimed.
//
// The bearer needs runs:run to claim and runs:kill to release.
func (c *Client) Hold(ctx context.Context, folder, reason string) (HoldOutcome, error) {
	return call[HoldOutcome](ctx, c, http.MethodPost, "/v1/holds",
		HoldRequest{Folder: types.Folder(folder), Reason: reason}, true, "hold outcome")
}

// ReleaseHold frees a hold's run slot. It is literally DELETE
// /v1/runs/{run_id}, the existing kill route — a hold IS a run, and Kill
// already dispatches by the spawn's recorded kind, so releasing needs no
// route of its own. Idempotent: releasing an already-expired hold is a 200.
func (c *Client) ReleaseHold(ctx context.Context, runID string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete,
		"/v1/runs/"+url.PathEscape(runID), nil, false, "")
	return err
}

// RecentSessions GETs /v1/sessions/recent?folder=&n= — the n newest
// session_log rows for folder (full-fielded). routd calls it for the
// new_session continuity hint + inspect_session tool instead of opening
// runed.db. A transport failure / non-2xx surfaces as the error; the caller
// treats it as "no prior session" (advisory, never fatal).
func (c *Client) RecentSessions(ctx context.Context, folder string, n int) (RecentSessionsResponse, error) {
	q := url.Values{}
	q.Set("folder", folder)
	if n > 0 {
		q.Set("n", strconv.Itoa(n))
	}
	return call[RecentSessionsResponse](ctx, c, http.MethodGet,
		"/v1/sessions/recent?"+q.Encode(), nil, false, "recent sessions")
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

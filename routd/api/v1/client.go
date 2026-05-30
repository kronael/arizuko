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
)

// Client is a thin HTTP client for routd's /v1/turns/{turn_id}/* callback
// surface. runed holds one to forward the agent's reply/send/... tool
// calls back into routd (the sole appender). The bearer is the agent's
// brokered capability token — runed forwards it verbatim, never re-signs.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a routd callback client. The per-call token is passed
// to each method (it is the agent's brokered token, distinct per spawn),
// not stored on the Client.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: http.DefaultClient}
}

func (c *Client) post(ctx context.Context, path, token, idemKey string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e Err
		_ = json.Unmarshal(raw, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Error, Msg: e.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Reply forwards the agent reply tool to POST /v1/turns/{turn_id}/reply.
func (c *Client) Reply(ctx context.Context, turnID, token, idemKey string, r ReplyRequest) (SendResult, error) {
	var out SendResult
	err := c.post(ctx, "/v1/turns/"+turnID+"/reply", token, idemKey, r, &out)
	return out, err
}

// Send forwards the agent send tool to POST /v1/turns/{turn_id}/send.
func (c *Client) Send(ctx context.Context, turnID, token, idemKey string, r ReplyRequest) (SendResult, error) {
	var out SendResult
	err := c.post(ctx, "/v1/turns/"+turnID+"/send", token, idemKey, r, &out)
	return out, err
}

// Document forwards send_file to POST /v1/turns/{turn_id}/document.
func (c *Client) Document(ctx context.Context, turnID, token, idemKey string, r DocumentRequest) (SendResult, error) {
	var out SendResult
	err := c.post(ctx, "/v1/turns/"+turnID+"/document", token, idemKey, r, &out)
	return out, err
}

// Like forwards like (and dislike, with reaction=👎) to /v1/turns/{id}/like.
func (c *Client) Like(ctx context.Context, turnID, token, idemKey string, r ReactionRequest) error {
	return c.post(ctx, "/v1/turns/"+turnID+"/like", token, idemKey, r, nil)
}

// Verb forwards a bare-verb mutation (edit/delete/pin/unpin) to its path.
func (c *Client) Verb(ctx context.Context, turnID, verb, token, idemKey string, body any) error {
	return c.post(ctx, "/v1/turns/"+turnID+"/"+verb, token, idemKey, body, nil)
}

// Result forwards submit_turn to POST /v1/turns/{turn_id}/result.
func (c *Client) Result(ctx context.Context, turnID, token, idemKey string, r TurnResult) (TurnResultAck, error) {
	var out TurnResultAck
	err := c.post(ctx, "/v1/turns/"+turnID+"/result", token, idemKey, r, &out)
	return out, err
}

// History calls GET /v1/turns/{turn_id}/history.
func (c *Client) History(ctx context.Context, turnID, token, jid, before, q string, limit int) (HistoryResponse, error) {
	var out HistoryResponse
	qs := url.Values{}
	qs.Set("jid", jid)
	if before != "" {
		qs.Set("before", before)
	}
	if q != "" {
		qs.Set("q", q)
	}
	if limit > 0 {
		qs.Set("limit", strconv.Itoa(limit))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/v1/turns/"+turnID+"/history?"+qs.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	err = c.do(req, &out)
	return out, err
}

// APIError is a non-2xx response from routd carrying the decoded Err
// envelope.
type APIError struct {
	Status int
	Code   string
	Msg    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("routd %d %s: %s", e.Status, e.Code, e.Msg)
}

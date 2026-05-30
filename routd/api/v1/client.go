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

// get issues a token-bearing GET to a non-turn path with query qs.
func (c *Client) get(ctx context.Context, path string, qs url.Values, token string, out any) error {
	u := c.BaseURL + path
	if enc := qs.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.do(req, out)
}

// InspectMessages calls GET /v1/messages/inspect (the agent's whole-chat DB read).
func (c *Client) InspectMessages(ctx context.Context, token, jid, before string, limit int) (MessagesResponse, error) {
	var out MessagesResponse
	qs := url.Values{}
	qs.Set("jid", jid)
	if before != "" {
		qs.Set("before", before)
	}
	if limit > 0 {
		qs.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/v1/messages/inspect", qs, token, &out)
}

// ThreadMessages calls GET /v1/messages/thread (one (jid,topic) slice).
func (c *Client) ThreadMessages(ctx context.Context, token, jid, topic, before string, limit int) (MessagesResponse, error) {
	var out MessagesResponse
	qs := url.Values{}
	qs.Set("jid", jid)
	qs.Set("topic", topic)
	if before != "" {
		qs.Set("before", before)
	}
	if limit > 0 {
		qs.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/v1/messages/thread", qs, token, &out)
}

// FindMessages calls GET /v1/messages/find (FTS5 search).
func (c *Client) FindMessages(ctx context.Context, token, query, scope, sender, since string, limit int) (FindResponse, error) {
	var out FindResponse
	qs := url.Values{}
	qs.Set("query", query)
	if scope != "" {
		qs.Set("scope", scope)
	}
	if sender != "" {
		qs.Set("sender", sender)
	}
	if since != "" {
		qs.Set("since", since)
	}
	if limit > 0 {
		qs.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/v1/messages/find", qs, token, &out)
}

// ResolveRouting calls GET /v1/routing/resolve. folder="" → DefaultFolderForJID
// (Folder set); folder!="" → JIDRoutedToFolder (Routed set).
func (c *Client) ResolveRouting(ctx context.Context, token, jid, folder string) (RoutingResolveResponse, error) {
	var out RoutingResolveResponse
	qs := url.Values{}
	qs.Set("jid", jid)
	if folder != "" {
		qs.Set("folder", folder)
	}
	return out, c.get(ctx, "/v1/routing/resolve", qs, token, &out)
}

// ErroredChats calls GET /v1/routing/errored.
func (c *Client) ErroredChats(ctx context.Context, token, folder string) (ErroredChatsResponse, error) {
	var out ErroredChatsResponse
	qs := url.Values{}
	if folder != "" {
		qs.Set("folder", folder)
	}
	return out, c.get(ctx, "/v1/routing/errored", qs, token, &out)
}

// GetEngagement calls GET /v1/engagement (engaged folder + thread anchor).
func (c *Client) GetEngagement(ctx context.Context, token, jid, topic string) (EngagementResponse, error) {
	var out EngagementResponse
	qs := url.Values{}
	qs.Set("jid", jid)
	qs.Set("topic", topic)
	return out, c.get(ctx, "/v1/engagement", qs, token, &out)
}

// SetEngagement calls POST /v1/engagement (engage/disengage).
func (c *Client) SetEngagement(ctx context.Context, token string, r EngagementRequest) error {
	return c.post(ctx, "/v1/engagement", token, "", r, nil)
}

// LogCost calls POST /v1/cost (one external-LLM cost_log row).
func (c *Client) LogCost(ctx context.Context, token string, r CostRequest) error {
	return c.post(ctx, "/v1/cost", token, "", r, nil)
}

// GetSession calls GET /v1/sessions (resume session id for (folder,topic)).
func (c *Client) GetSession(ctx context.Context, token, folder, topic string) (string, error) {
	var out SessionResponse
	qs := url.Values{}
	qs.Set("folder", folder)
	qs.Set("topic", topic)
	return out.SessionID, c.get(ctx, "/v1/sessions", qs, token, &out)
}

// Routes CRUD — the agent's route-management tools federate here.

// ListRoutes calls GET /v1/routes.
func (c *Client) ListRoutes(ctx context.Context, token string) ([]Route, error) {
	var out []Route
	return out, c.get(ctx, "/v1/routes", nil, token, &out)
}

// GetRoute calls GET /v1/routes/{id}; ok=false on 404.
func (c *Client) GetRoute(ctx context.Context, token string, id int64) (Route, bool, error) {
	var out Route
	err := c.get(ctx, "/v1/routes/"+strconv.FormatInt(id, 10), nil, token, &out)
	if e, isAPI := err.(*APIError); isAPI && e.Status == http.StatusNotFound {
		return Route{}, false, nil
	}
	return out, err == nil, err
}

// AddRoute calls POST /v1/routes, returning the assigned id.
func (c *Client) AddRoute(ctx context.Context, token string, r Route) (int64, error) {
	var out Route
	if err := c.post(ctx, "/v1/routes", token, "", r, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// SetRoutes calls PUT /v1/routes (folder-scoped bulk replace).
func (c *Client) SetRoutes(ctx context.Context, token string, routes []Route) error {
	return c.put(ctx, "/v1/routes", token, routes, nil)
}

// DeleteRoute calls DELETE /v1/routes/{id}.
func (c *Client) DeleteRoute(ctx context.Context, token string, id int64) error {
	return c.method(ctx, http.MethodDelete, "/v1/routes/"+strconv.FormatInt(id, 10), token, nil, nil)
}

// WebRoutes CRUD — the agent's web-route tools federate here.

// ListWebRoutes calls GET /v1/web_routes?folder=.
func (c *Client) ListWebRoutes(ctx context.Context, token, folder string) ([]WebRoute, error) {
	var out []WebRoute
	qs := url.Values{}
	if folder != "" {
		qs.Set("folder", folder)
	}
	return out, c.get(ctx, "/v1/web_routes", qs, token, &out)
}

// WebRouteOwner calls GET /v1/web_routes?path_prefix= → owning folder ("" none).
func (c *Client) WebRouteOwner(ctx context.Context, token, pathPrefix string) (string, error) {
	var out struct {
		Owner string `json:"owner"`
	}
	qs := url.Values{}
	qs.Set("path_prefix", pathPrefix)
	return out.Owner, c.get(ctx, "/v1/web_routes", qs, token, &out)
}

// PutWebRoute calls PUT /v1/web_routes (set_web_route).
func (c *Client) PutWebRoute(ctx context.Context, token string, r WebRoute) error {
	return c.put(ctx, "/v1/web_routes", token, r, nil)
}

// DeleteWebRoute calls DELETE /v1/web_routes (del_web_route).
func (c *Client) DeleteWebRoute(ctx context.Context, token string, r WebRoute) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	return out.Deleted, c.method(ctx, http.MethodDelete, "/v1/web_routes", token, r, &out)
}

// put/method are token-bearing JSON requests for the non-POST verbs.
func (c *Client) put(ctx context.Context, path, token string, body, out any) error {
	return c.method(ctx, http.MethodPut, path, token, body, out)
}

func (c *Client) method(ctx context.Context, verb, path, token string, body, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, verb, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return c.do(req, out)
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

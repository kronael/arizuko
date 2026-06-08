package routd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OnbodClient is routd's federation to onbod's invite/gate admin surface. onbod
// OWNS invites + onboarding_gates; routd's /invite and /gate slash commands reach
// them over HTTP instead of touching the tables. nil client (ONBOD_URL unset / no
// service key) → the commands report the federation gap. Each method returns a
// human-readable line for the steering ack.
type OnbodClient interface {
	CreateInvite(targetGlob string, maxUses int) (token string, err error)
	CreateInviteFull(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (Invite, error)
	ListInvites(issuedBy string) ([]Invite, error)
	RevokeInvite(token string) error
	InsertOnboarding(jid string) error
	ListGates() ([]GateRow, error)
	PutGate(gate string, limitPerDay int) error
	DeleteGate(gate string) error
	EnableGate(gate string, enabled bool) error
}

// Invite is one invite row decoded from onbod's invite JSON (onbod's inviteJSON
// shape). routd-local so the MCP invite tools see the full row (token, target,
// issuer, expiry, use counts) — not just the token CreateInvite returns.
type Invite struct {
	Token       string     `json:"token"`
	TargetGlob  string     `json:"target_glob"`
	IssuedBySub string     `json:"issued_by_sub"`
	IssuedAt    time.Time  `json:"issued_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MaxUses     int        `json:"max_uses"`
	UsedCount   int        `json:"used_count"`
}

// GateRow is one onboarding gate as returned by onbod's GET /v1/gates.
type GateRow struct {
	Gate        string `json:"gate"`
	LimitPerDay int    `json:"limit_per_day"`
	Enabled     bool   `json:"enabled"`
}

// httpOnbod calls onbod's /v1/* admin endpoints with routd's service token (the
// same service:routd bearer the runed/identity clients use). Empty onbodURL →
// NewOnbodClient returns nil.
type httpOnbod struct {
	url   string
	token func(context.Context) (string, error)
	c     *http.Client
}

// NewOnbodClient builds a client against onbodURL, authenticating with token
// (routd's service-token source). Empty onbodURL → nil (no client; /invite +
// /gate then report the federation gap).
func NewOnbodClient(onbodURL string, token func(context.Context) (string, error)) OnbodClient {
	if onbodURL == "" {
		return nil
	}
	return &httpOnbod{
		url:   strings.TrimRight(onbodURL, "/"),
		token: token,
		c:     &http.Client{Timeout: 10 * time.Second},
	}
}

// do issues a bearer-authenticated JSON request against onbod, decoding a 2xx
// body into out (when non-nil). A non-2xx is an error carrying the status.
func (o *httpOnbod) do(method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := o.token(ctx)
	if err != nil {
		return err
	}
	var rdr *bytes.Reader
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return merr
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.url+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("onbod %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (o *httpOnbod) CreateInvite(targetGlob string, maxUses int) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := o.do(http.MethodPost, "/v1/invites",
		map[string]any{"target_glob": targetGlob, "max_uses": maxUses, "issued_by_sub": "routd"}, &out)
	return out.Token, err
}

// CreateInviteFull mints an invite and decodes onbod's full invite JSON (not
// just the token). issuedBySub records the agent folder so ListInvites can
// scope to the issuer; expiresAt is optional (nil → no expiry).
func (o *httpOnbod) CreateInviteFull(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (Invite, error) {
	body := map[string]any{"target_glob": targetGlob, "max_uses": maxUses, "issued_by_sub": issuedBySub}
	if expiresAt != nil {
		body["expires_at"] = expiresAt.Format(time.RFC3339)
	}
	var out Invite
	err := o.do(http.MethodPost, "/v1/invites", body, &out)
	return out, err
}

// ListInvites returns invites onbod has on record for issuedBy (GET
// /v1/invites?issued_by=). Empty issuedBy lists all (operator surface); the MCP
// twin always passes "agent:<folder>" so an agent sees only its own.
func (o *httpOnbod) ListInvites(issuedBy string) ([]Invite, error) {
	var out struct {
		Invites []Invite `json:"invites"`
	}
	path := "/v1/invites"
	if issuedBy != "" {
		path += "?issued_by=" + url.QueryEscape(issuedBy)
	}
	if err := o.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Invites, nil
}

// RevokeInvite deletes an invite by token (DELETE /v1/invites/{token}). onbod's
// DELETE is token-only; the MCP twin enforces folder ownership before calling.
func (o *httpOnbod) RevokeInvite(token string) error {
	return o.do(http.MethodDelete, "/v1/invites/"+url.PathEscape(token), nil, nil)
}

// InsertOnboarding records a chat-initiated onboarding row for an unrouted JID
// via onbod's POST /v1/onboarding (onbod OWNS the table). Idempotent onbod-side.
func (o *httpOnbod) InsertOnboarding(jid string) error {
	return o.do(http.MethodPost, "/v1/onboarding", map[string]any{"jid": jid}, nil)
}

func (o *httpOnbod) ListGates() ([]GateRow, error) {
	var out struct {
		Gates []GateRow `json:"gates"`
	}
	if err := o.do(http.MethodGet, "/v1/gates", nil, &out); err != nil {
		return nil, err
	}
	return out.Gates, nil
}

func (o *httpOnbod) PutGate(gate string, limitPerDay int) error {
	return o.do(http.MethodPut, "/v1/gates/"+url.PathEscape(gate),
		map[string]any{"limit_per_day": limitPerDay}, nil)
}

func (o *httpOnbod) DeleteGate(gate string) error {
	return o.do(http.MethodDelete, "/v1/gates/"+url.PathEscape(gate), nil, nil)
}

func (o *httpOnbod) EnableGate(gate string, enabled bool) error {
	return o.do(http.MethodPut, "/v1/gates/"+url.PathEscape(gate),
		map[string]any{"enabled": enabled}, nil)
}

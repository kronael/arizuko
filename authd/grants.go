package main

// HTTP GrantsFetcher: authd is not the grants authority (gated is — spec 5/1
// § Login-time scope snapshot). At session issuance / issuer-mint / refresh,
// authd fetches the target's scope ceiling over one HTTP call:
//
//   GET <GRANTS_URL>/v1/users/{bareSub}/scopes   Authorization: Bearer <service:authd>
//   → 200 {"scope":[...],"folder":"..."}   → 404 {"error":"no_grants"}
//
// authd authenticates with its own self-minted service:authd token (scope
// grants:read). It holds the signing key, so it signs that token locally per
// call (a local ES256 sign, no network hop, no service_keys seed row — spec
// 5/1: "authd is the one daemon that needs no bootstrap to obtain a service
// identity"). When GRANTS_URL is unset the fetcher is nil and behavior is
// unchanged (every session empty-scope; suitable for an auth-only deployment).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/obs"
)

// httpGrants resolves a bare sub's scope ceiling against the grants backend.
type httpGrants struct {
	a   *Authd // self-mints the service:authd bearer (authd holds the signing key)
	url string // GRANTS_URL, trailing slash trimmed
	c   *http.Client
}

// newHTTPGrants builds a fetcher against grantsURL, or nil when grantsURL is
// empty (leave grants unwired — current empty-scope behavior).
func newHTTPGrants(a *Authd, grantsURL string) *httpGrants {
	if grantsURL == "" {
		return nil
	}
	return &httpGrants{
		a:   a,
		url: strings.TrimRight(grantsURL, "/"),
		c:   &http.Client{Timeout: 10 * time.Second},
	}
}

// FetchGrants returns the bare sub's scope + folder. A 404 maps to ErrNoGrants
// (sub has no grant rows — authenticated-but-unauthorized); any other non-200
// or transport error is "backend down" so callers fail closed.
func (g *httpGrants) FetchGrants(ctx context.Context, bareSub string) (GrantsSnapshot, error) {
	token, err := g.a.MintForSubject("service:authd", "service", nil, serviceGrants["service:authd"], "")
	if err != nil {
		return GrantsSnapshot{}, err
	}
	endpoint := g.url + "/v1/users/" + url.PathEscape(bareSub) + "/scopes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GrantsSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	obs.InjectRequest(ctx, req)
	resp, err := g.c.Do(req)
	if err != nil {
		return GrantsSnapshot{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Scope  []string `json:"scope"`
			Folder string   `json:"folder"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return GrantsSnapshot{}, err
		}
		return GrantsSnapshot{Scope: out.Scope, Folder: out.Folder}, nil
	case http.StatusNotFound:
		return GrantsSnapshot{}, ErrNoGrants
	default:
		return GrantsSnapshot{}, fmt.Errorf("grants backend: status %d", resp.StatusCode)
	}
}

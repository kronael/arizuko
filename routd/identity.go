package routd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/ipc"
)

// IdentityResolver resolves a platform sender sub to its canonical identity and
// the full set of subs that identity claims. authd OWNS identity and serves GET
// /v1/identities/{sub}; routd snapshots it over HTTP.
//
// Three outcomes, all distinguishable (BUGS N2): claimed (`true, nil`),
// genuinely unclaimed (`false, nil`), and COULD NOT ANSWER (`false, err` —
// authd unreachable, 401, undecodable). The third used to fold into the second,
// which is fine for an advisory tool but not for a caller that wants to act on
// the answer: "this sub is its own canonical" and "authd did not reply" are
// opposite facts and must not share a return value.
type IdentityResolver interface {
	Resolve(sub string) (ipc.Identity, []string, bool, error)
}

// httpIdentity calls authd's identity endpoint with routd's service token.
type httpIdentity struct {
	url   string                                // AUTHD_URL, trailing slash trimmed
	token func(context.Context) (string, error) // routd's service:routd bearer
	c     *http.Client
}

// NewIdentityResolver builds a resolver against authdURL, authenticating with
// token (routd's service-token source). Empty authdURL → nil (no resolver;
// inspect_identity then answers unclaimed).
func NewIdentityResolver(authdURL string, token func(context.Context) (string, error)) IdentityResolver {
	if authdURL == "" {
		return nil
	}
	return &httpIdentity{
		url:   strings.TrimRight(authdURL, "/"),
		token: token,
		c:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Resolve issues GET /v1/identities/{sub}. A claimed sub returns its identity +
// linked subs and a nil error; a 404 is a genuine "unclaimed" (false, nil).
// Anything that stops authd answering — token, transport, other non-200, decode
// — returns an error, so a caller can tell a real answer from no answer.
func (g *httpIdentity) Resolve(sub string) (ipc.Identity, []string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := g.token(ctx)
	if err != nil {
		return ipc.Identity{}, nil, false, fmt.Errorf("identity token: %w", err)
	}
	endpoint := g.url + "/v1/identities/" + url.PathEscape(sub)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ipc.Identity{}, nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.c.Do(req)
	if err != nil {
		return ipc.Identity{}, nil, false, fmt.Errorf("identity request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ipc.Identity{}, nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return ipc.Identity{}, nil, false, fmt.Errorf("identity: authd returned %d", resp.StatusCode)
	}
	var out struct {
		Identity *ipc.Identity `json:"identity"`
		Subs     []string      `json:"subs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ipc.Identity{}, nil, false, fmt.Errorf("identity decode: %w", err)
	}
	if out.Identity == nil {
		return ipc.Identity{}, nil, false, nil
	}
	return *out.Identity, out.Subs, true, nil
}

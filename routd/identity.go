package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/ipc"
)

// IdentityResolver resolves a platform sender sub to its canonical identity and
// the full set of subs that identity claims. authd OWNS identity and serves GET
// /v1/identities/{sub}; routd snapshots it over HTTP for the inspect_identity
// tool. nil resolver / unreachable authd / unclaimed sub → (zero, nil, false),
// the unclaimed shape the tool renders.
type IdentityResolver interface {
	Resolve(sub string) (ipc.Identity, []string, bool)
}

// httpIdentity calls authd's identity endpoint with routd's service token.
type httpIdentity struct {
	url   string                              // AUTHD_URL, trailing slash trimmed
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
// linked subs; unclaimed → (zero, nil, false). Any transport/auth error or
// non-200 also returns the unclaimed shape — advisory only, never an error path.
func (g *httpIdentity) Resolve(sub string) (ipc.Identity, []string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := g.token(ctx)
	if err != nil {
		return ipc.Identity{}, nil, false
	}
	endpoint := g.url + "/v1/identities/" + url.PathEscape(sub)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ipc.Identity{}, nil, false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.c.Do(req)
	if err != nil {
		return ipc.Identity{}, nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ipc.Identity{}, nil, false
	}
	var out struct {
		Identity *ipc.Identity `json:"identity"`
		Subs     []string      `json:"subs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Identity == nil {
		return ipc.Identity{}, nil, false
	}
	return *out.Identity, out.Subs, true
}

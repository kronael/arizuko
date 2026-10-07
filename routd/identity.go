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

// IdentityResolver resolves a platform sender sub to its canonical identity
// and the subs it claims; authd OWNS identity, routd snapshots it over HTTP.
// Three outcomes, all distinguishable (BUGS N2): claimed (`true, nil`),
// genuinely unclaimed (`false, nil`), and COULD NOT ANSWER (`false, err`) —
// "this sub is its own canonical" and "authd did not reply" are opposite facts
// and must not share a return value.
type IdentityResolver interface {
	Resolve(sub string) (ipc.Identity, []string, bool, error)
}

// httpIdentity calls authd's identity endpoint with routd's service token.
type httpIdentity struct {
	url   string                                // AUTHD_URL, trailing slash trimmed
	token func(context.Context) (string, error) // routd's service:routd bearer
	c     *http.Client
}

// NewIdentityResolver builds a resolver against authdURL; empty → nil.
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

// Resolve issues GET /v1/identities/{sub}: a 404 is a genuine "unclaimed";
// anything that stops authd answering — token, transport, other non-200,
// decode — is an error, so a caller can tell a real answer from no answer.
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
	defer func() { _ = resp.Body.Close() }()
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

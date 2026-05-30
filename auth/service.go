package auth

// Service-token bootstrap (spec 5/1 § Service bootstrap). A daemon exchanges
// its AUTHD_SERVICE_KEY for a short-lived `service:<name>` ES256 JWT at boot
// and keeps it refreshed ~1 min before expiry, the way the JWKS RemoteKeySet
// refreshes — no per-request hop. This replaces the static *_SERVICE_TOKEN env
// pattern. The daemon presents Token() on daemon→daemon calls; it never holds a
// signing key (only authd signs).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// refreshLead is how long before expiry the source re-exchanges.
const refreshLead = time.Minute

// TokenSource holds a daemon's current service token and refreshes it lazily.
// Token() returns a live token, exchanging on first use and re-exchanging once
// the cached one is within refreshLead of expiry. Safe for concurrent use.
type TokenSource struct {
	authdURL string
	daemon   string
	key      string
	http     *http.Client
	now      func() time.Time // injectable clock for tests

	mu      sync.Mutex
	token   string
	expires time.Time
}

// ServiceToken builds a TokenSource for daemon against authd, exchanging key
// (the daemon's AUTHD_SERVICE_KEY) for a service JWT. It does not exchange
// eagerly — the first Token() call performs the exchange so a daemon can
// construct the source before authd is reachable.
func ServiceToken(authdURL, daemon, key string) (*TokenSource, error) {
	if authdURL == "" || daemon == "" || key == "" {
		return nil, fmt.Errorf("service token: authdURL, daemon and key are all required")
	}
	return &TokenSource{
		authdURL: strings.TrimRight(authdURL, "/"),
		daemon:   daemon,
		key:      key,
		http:     &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
	}, nil
}

// Token returns a live service token, refreshing if the cached one is missing
// or within refreshLead of expiry.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token != "" && ts.now().Before(ts.expires.Add(-refreshLead)) {
		return ts.token, nil
	}
	tok, exp, err := ts.exchange(ctx)
	if err != nil {
		return "", err
	}
	ts.token, ts.expires = tok, exp
	return tok, nil
}

// exchange performs the POST /v1/service-token round-trip: secret in the
// Authorization header, daemon name in the body (spec 5/1 §435).
func (ts *TokenSource) exchange(ctx context.Context) (token string, expires time.Time, err error) {
	body, _ := json.Marshal(map[string]string{"daemon": ts.daemon})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ts.authdURL+"/v1/service-token", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ts.key)
	resp, err := ts.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("service-token exchange: status %d", resp.StatusCode)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("service-token exchange: empty token")
	}
	// The exchange response carries the token; its expiry is read from the
	// verified claim so the refresh schedule tracks the real exp, not a separate
	// field that could drift. expires_at is advisory only.
	exp := ts.now().Add(time.Hour) // fallback if exp is unreadable
	if claims, perr := unverifiedExp(out.Token); perr == nil {
		exp = claims
	} else if out.ExpiresAt != "" {
		if t, terr := time.Parse(time.RFC3339, out.ExpiresAt); terr == nil {
			exp = t
		}
	}
	return out.Token, exp, nil
}

// unverifiedExp reads the exp claim from a compact JWS without verifying the
// signature — used only to schedule refresh of a token authd just minted for us
// (the issuer is trusted; we hold no verify key here). Never an auth decision.
func unverifiedExp(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a compact JWS")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(c.Exp, 0), nil
}

package egress

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// upstream stands up a plain-HTTP origin and returns it.
func upstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream ok")
	}))
	t.Cleanup(s.Close)
	return s
}

// serve starts p on loopback and returns its URL.
func serve(t *testing.T, p *Proxy) *url.URL {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := p.Server(lis.Addr().String())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })
	u, _ := url.Parse("http://" + lis.Addr().String())
	return u
}

// through returns a client that sends everything to the proxy at u.
func through(u *url.URL) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
	}
}

// asLocalhost rewrites "http://127.0.0.1:N/" to "http://localhost:N/" so the
// proxy sees a NAME to match: match.Host deliberately skips IP entries, so a
// test cannot allowlist "127.0.0.1" and must go through the resolver.
func asLocalhost(raw string) string {
	u, _ := url.Parse(raw)
	_, port, _ := net.SplitHostPort(u.Host)
	return u.Scheme + "://localhost:" + port + "/"
}

// TestAllowlistedHostPasses — the property this proxy exists for, allow half:
// a host on the list reaches its upstream.
func TestAllowlistedHostPasses(t *testing.T) {
	up := upstream(t)
	c := through(serve(t, New([]string{"localhost"}, "")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "upstream ok") {
		t.Errorf("body = %q, want the upstream's", body)
	}
}

// TestNonAllowlistedHostRefused — the deny half: a host that is NOT on the
// list is refused, and the refusal happens before any upstream connect.
func TestNonAllowlistedHostRefused(t *testing.T) {
	up := upstream(t)
	c := through(serve(t, New([]string{"api.anthropic.com"}, "")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (proxy deny)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "upstream ok") {
		t.Error("denied request still reached the upstream")
	}
}

// TestEmptyAllowlistDeniesEverything — no rules means no egress, never
// "no rules means no filter".
func TestEmptyAllowlistDeniesEverything(t *testing.T) {
	up := upstream(t)
	c := through(serve(t, New(nil, "")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestForeignPeerRefused — a neighbour on the same network cannot borrow this
// proxy's allowlist. The proxy is pinned to one client; every other peer is
// refused even for a host that IS allowlisted.
func TestForeignPeerRefused(t *testing.T) {
	up := upstream(t)
	// 127.0.0.2 is loopback but not the address the test client dials from.
	c := through(serve(t, New([]string{"localhost"}, "127.0.0.2")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (foreign peer)", resp.StatusCode)
	}
}

// TestPinnedPeerPasses — the same pin accepts the client it names.
func TestPinnedPeerPasses(t *testing.T) {
	up := upstream(t)
	c := through(serve(t, New([]string{"localhost"}, "127.0.0.1")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the pinned client", resp.StatusCode)
	}
}

// TestWildcardAllowsEverything — the `*` entry the egress grant appends.
func TestWildcardAllowsEverything(t *testing.T) {
	up := upstream(t)
	c := through(serve(t, New([]string{"*"}, "")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

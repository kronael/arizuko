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
	// Every upstream in this file is an httptest server on 127.0.0.1, which is
	// the local-development case SetAllowLoopback exists for. The address
	// checks themselves are covered in address_test.go, on their own.
	p.SetAllowLoopback(true)
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

// mustNew is New with the validation the tests don't exercise unwrapped —
// every list in this file is valid by construction.
func mustNew(t *testing.T, allow, deny []string, client string) *Proxy {
	t.Helper()
	p, err := New(allow, deny, client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// asLocalhost rewrites "http://127.0.0.1:N/" to "http://localhost:N/" so the
// proxy sees a NAME to match: an IP entry is refused at construction, so a
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
	c := through(serve(t, mustNew(t, []string{"localhost"}, nil, "")))

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
	c := through(serve(t, mustNew(t, []string{"api.anthropic.com"}, nil, "")))

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
	c := through(serve(t, mustNew(t, nil, nil, "")))

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
	c := through(serve(t, mustNew(t, []string{"localhost"}, nil, "127.0.0.2")))

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
	c := through(serve(t, mustNew(t, []string{"localhost"}, nil, "127.0.0.1")))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the pinned client", resp.StatusCode)
	}
}

// TestAllowAllAllowsEverything — the explicit mode the elevated egress grant
// rides. It replaced the magic "*" list entry (spec 6/25 1b): a bare "*" in a
// list is a construction error now (TestNewRefusesInvalidRules).
func TestAllowAllAllowsEverything(t *testing.T) {
	up := upstream(t)
	p := mustNew(t, nil, nil, "")
	p.SetAllowAll(true)
	c := through(serve(t, p))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestDenyBeatsAllow — through the real handler: a denied name is refused
// even when allow-all would pass it. The subdomain-carve shapes (deny
// internal.example.com out of allow example.com) are pinned in match's own
// tests; this proves the proxy consults deny FIRST.
func TestDenyBeatsAllow(t *testing.T) {
	up := upstream(t)
	p := mustNew(t, nil, []string{"localhost"}, "")
	p.SetAllowAll(true)
	c := through(serve(t, p))

	resp, err := c.Get(asLocalhost(up.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (deny beats allow-all)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "upstream ok") {
		t.Error("denied request still reached the upstream")
	}
}

// TestNewRefusesInvalidRules — the boundary that accepts a list is where a
// rule that can never match dies, loudly, one refusal per defect class.
func TestNewRefusesInvalidRules(t *testing.T) {
	bad := [][2][]string{
		{{"*"}, nil},                                 // allow-all is a mode, not an entry
		{{"https://example.com"}, nil},               // scheme
		{{"example.com:443"}, nil},                   // port
		{{"example.com/path"}, nil},                  // path
		{{"user@example.com"}, nil},                  // credentials
		{{"10.0.0.1"}, nil},                          // IP never matches a name
		{{"api.*.example.com"}, nil},                 // only a leading *. wildcard
		{{"example.com", "example.com"}, nil},        // duplicate
		{{"example.com"}, []string{"*.example.com"}}, // same host in both lists
	}
	for _, lists := range bad {
		if _, err := New(lists[0], lists[1], ""); err == nil {
			t.Errorf("New(%v, %v) accepted a rule that can never work", lists[0], lists[1])
		}
	}
}

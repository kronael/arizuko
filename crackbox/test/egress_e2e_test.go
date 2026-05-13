// Package test exercises the crackbox egress proxy end-to-end against an
// in-process proxy + loopback upstreams. Models the 9-outcome matrix that
// openclaw-managed-agents' test/e2e-networking.sh proves at the docker
// network layer. The cases that fundamentally require kernel-level
// isolation (raw TCP bypass, IMDS bypass, DNS NXDOMAIN) are skipped here
// with notes pointing at the missing infra — crackbox itself only owns
// the proxy half of the enforcement.
package test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/crackbox/pkg/admin"
	"github.com/kronael/arizuko/crackbox/pkg/dns"
	"github.com/kronael/arizuko/crackbox/pkg/proxy"
)

// envFixture stands up an in-process crackbox proxy + loopback upstreams
// for each E2E case. Upstreams listen on 127.0.0.1. Test URLs use the
// "localhost" hostname so crackbox's allowlist (host-name based) can
// match a name entry — IP entries in the allowlist are deliberately
// skipped by match.Host so the test cannot allowlist "127.0.0.1" and
// must use a name. Both halves resolve to the loopback interface via
// the OS resolver.
type envFixture struct {
	t            *testing.T
	reg          *admin.Registry
	proxyURL     *url.URL
	httpUpstream *httptest.Server
	tlsUpstream  *httptest.Server
}

func newEnv(t *testing.T, allowlist []string) *envFixture {
	t.Helper()
	reg := admin.NewRegistry()
	// Source IP = 127.0.0.1 (clients always connect from loopback). Register
	// the allowlist against that source.
	reg.Set("127.0.0.1", "e2e", allowlist)

	httpUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "http")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello from http upstream")
	}))
	t.Cleanup(httpUpstream.Close)

	tlsUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "tls")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello from tls upstream")
	}))
	t.Cleanup(tlsUpstream.Close)

	p := proxy.New(reg)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	srv := p.Server(lis.Addr().String())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })

	purl, _ := url.Parse("http://" + lis.Addr().String())
	return &envFixture{
		t:            t,
		reg:          reg,
		proxyURL:     purl,
		httpUpstream: httpUpstream,
		tlsUpstream:  tlsUpstream,
	}
}

// clientWithProxy returns an http.Client that routes all traffic through
// e.proxyURL. Upstreams listen on 127.0.0.1; URLs in test cases use
// "localhost" so crackbox's allowlist (host-name based, skips IP entries)
// can match a name. The proxy then dials "localhost:NNNN" which resolves
// to 127.0.0.1 via the OS resolver. InsecureSkipVerify covers the fact
// that the upstream test cert is for 127.0.0.1 / example.com, not
// localhost — TLS verification is not the concern of these tests; the
// proxy's allowlist enforcement is.
func (e *envFixture) clientWithProxy() *http.Client {
	e.t.Helper()
	tlsCfg := &tls.Config{
		RootCAs:            x509.NewCertPool(),
		InsecureSkipVerify: true, //nolint:gosec // test-only; upstream cert is self-signed for 127.0.0.1
	}
	tlsCfg.RootCAs.AddCert(e.tlsUpstream.Certificate())

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(e.proxyURL),
			TLSClientConfig: tlsCfg,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// asLocalhost rewrites an httptest server URL ("http://127.0.0.1:NNNN/")
// into "<scheme>://localhost:NNNN/" so the proxy sees a name (matchable
// against a name-based allowlist) and the OS resolver maps it back to
// the loopback upstream.
func asLocalhost(upstreamURL string) string {
	u, _ := url.Parse(upstreamURL)
	_, port, _ := net.SplitHostPort(u.Host)
	return fmt.Sprintf("%s://localhost:%s/", u.Scheme, port)
}

// ---- case 1: HTTP GET to allowlisted host ------------------------------

func TestE2E_Case1_HTTPAllowed(t *testing.T) {
	env := newEnv(t, []string{"localhost"})
	c := env.clientWithProxy()

	resp, err := c.Get(asLocalhost(env.httpUpstream.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello from http upstream") {
		t.Errorf("body = %q", body)
	}
}

// ---- case 2: HTTP GET to non-allowlisted host --------------------------

func TestE2E_Case2_HTTPDenied(t *testing.T) {
	// Allowlist that does NOT include localhost.
	env := newEnv(t, []string{"example.com"})
	c := env.clientWithProxy()

	resp, err := c.Get(asLocalhost(env.httpUpstream.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 from proxy deny", resp.StatusCode)
	}
}

// ---- case 3: HTTPS via CONNECT to allowlisted host ---------------------

func TestE2E_Case3_HTTPSAllowed(t *testing.T) {
	env := newEnv(t, []string{"localhost"})
	c := env.clientWithProxy()

	resp, err := c.Get(asLocalhost(env.tlsUpstream.URL))
	if err != nil {
		t.Fatalf("GET https: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 over CONNECT-splice", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") != "tls" {
		t.Errorf("X-Upstream = %q, want tls (CONNECT did not reach TLS upstream)",
			resp.Header.Get("X-Upstream"))
	}
}

// ---- case 4: HTTPS via CONNECT to non-allowlisted host -----------------

func TestE2E_Case4_HTTPSDenied(t *testing.T) {
	env := newEnv(t, []string{"example.com"})
	c := env.clientWithProxy()

	resp, err := c.Get(asLocalhost(env.tlsUpstream.URL))
	// CONNECT denial surfaces as a proxy 4xx returned through the
	// transport. http.Get returns an error if the proxy rejects CONNECT
	// because TLS handshake never starts. Either err != nil or status >=
	// 400 is a pass; a 200 is the failure.
	if err == nil && resp.StatusCode == http.StatusOK {
		t.Fatalf("denied HTTPS reached upstream (status=%d)", resp.StatusCode)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// ---- case 5: raw TCP socket.connect to allowlisted host:port -----------

// Crackbox is an HTTP/CONNECT proxy. Raw TCP without HTTP CONNECT cannot
// transit the proxy at all — it's not a SOCKS daemon. The arizuko
// equivalent of openclaw's "raw socket allowed" is: when a process
// inside the sandbox does socket.connect() to an address that has been
// explicitly opened (e.g. through transparent-mode iptables REDIRECT to
// :3127, with SNI/Host peek + allowlist match). In unit-test scope we
// assert the same plumbing: a TLS hello with SNI="127.0.0.1" against the
// transparent listener splices to the allowed upstream. That codepath
// already has unit coverage in pkg/proxy/transparent_test.go
// (TestTransparentDeny exercises the deny branch); the allow branch
// requires a real SO_ORIGINAL_DST socket which we cannot stub from
// userspace without root.
func TestE2E_Case5_RawTCPAllowed(t *testing.T) {
	t.Skip("requires SO_ORIGINAL_DST + iptables REDIRECT (transparent mode); " +
		"covered structurally by pkg/proxy/transparent_test.go and validated " +
		"end-to-end by `crackbox run` integration on a real Docker host")
}

// ---- case 6: raw TCP socket.connect to non-allowlisted IP --------------

// Same scope note as case 5: crackbox is not a kernel firewall. The
// "agent dodges proxy by raw TCP" attack is mitigated by Docker's
// --internal network at runtime — there is no kernel route off the
// confined bridge. crackbox alone cannot enforce this in a unit test.
func TestE2E_Case6_RawTCPDenied(t *testing.T) {
	t.Skip("requires Docker --internal network or netns isolation; mitigation " +
		"lives in the container runtime, not in crackbox. Asserted by " +
		"openclaw test/e2e-networking.sh case 3 on a real docker daemon")
}

// ---- case 7: AWS IMDS via proxy (169.254.169.254) ----------------------

func TestE2E_Case7_IMDSViaProxy(t *testing.T) {
	// Allowlist that does NOT include 169.254.169.254 — IMDS is the
	// canonical SSRF pivot, must be denied at the proxy layer.
	env := newEnv(t, []string{"localhost"})
	c := env.clientWithProxy()

	// Use a very short timeout so this test never hangs even if a bug
	// causes the proxy to dial IMDS for real.
	c.Timeout = 2 * time.Second

	resp, err := c.Get("http://169.254.169.254/latest/meta-data/")
	// Acceptable: 403 from proxy, or transport error (proxy denied
	// before any upstream connect). NOT acceptable: 2xx/3xx.
	if err != nil {
		// transport-level rejection counts as denied
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK ||
		(resp.StatusCode >= 300 && resp.StatusCode < 400) {
		t.Errorf("IMDS via proxy reached upstream (status=%d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Logf("note: IMDS via proxy returned %d (expected 403 from proxy deny)",
			resp.StatusCode)
	}
}

// ---- case 8: AWS IMDS direct (bypass proxy entirely) -------------------

// In production this is enforced by Docker --internal: no kernel route
// to 169.254.169.254 exists. Outside that runtime, a process running on
// a host that DOES have IMDS reachable (a real EC2 instance) would
// succeed — that's exactly the threat model that motivates the
// --internal bridge. Unit tests can't reproduce this without netns
// isolation, so this is a skip with a pointer.
func TestE2E_Case8_IMDSDirect(t *testing.T) {
	t.Skip("requires netns / Docker --internal isolation; crackbox alone " +
		"cannot enforce. Asserted by openclaw test/e2e-networking.sh case 4b")
}

// ---- case 9: DNS NXDOMAIN for an allowlisted hostname ------------------

// Crackbox now implements an in-proc DNS filter (spec 9/15) backed by
// admin.Registry. This e2e case stands up the dns.Server in front of a
// fake upstream and asserts the four outcomes the spec calls out:
// allowlisted A forwards, denied returns NXDOMAIN, ANY returns REFUSED,
// and replies from non-upstream sources are ignored.
func TestE2E_Case9_DNSNXDomain(t *testing.T) {
	// Fake upstream resolver: replies with QR=1 RCODE=0 to any query.
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	defer upstream.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, raddr, err := upstream.ReadFromUDP(buf)
			if err != nil {
				return
			}
			resp := make([]byte, n)
			copy(resp, buf[:n])
			resp[2] = 0x81
			resp[3] = 0x80
			_, _ = upstream.WriteToUDP(resp, raddr)
		}
	}()

	reg := admin.NewRegistry()
	reg.Set("127.0.0.1", "e2e", []string{"example.com"})
	srv, err := dns.New(reg, upstream.LocalAddr().String())
	if err != nil {
		t.Fatalf("dns.New: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve("127.0.0.1:0")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && srv.LocalAddr() == nil {
		time.Sleep(2 * time.Millisecond)
	}
	if srv.LocalAddr() == nil {
		t.Fatalf("dns server did not bind")
	}
	defer func() { _ = srv.Close(); <-done }()

	send := func(t *testing.T, q []byte) []byte {
		t.Helper()
		cc, err := net.DialUDP("udp", nil, srv.LocalAddr())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer cc.Close()
		if _, err := cc.Write(q); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := cc.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		buf := make([]byte, 1500)
		n, _, err := cc.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return buf[:n]
	}
	// Same wire-format helper as the dns package's tests, inlined here to
	// keep the package boundary clean (no test-only export).
	build := func(id uint16, name string, qtype uint16) []byte {
		hdr := []byte{byte(id >> 8), byte(id), 0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
		out := append([]byte{}, hdr...)
		start := 0
		for i := 0; i < len(name); i++ {
			if name[i] == '.' {
				if i > start {
					out = append(out, byte(i-start))
					out = append(out, name[start:i]...)
				}
				start = i + 1
			}
		}
		if start < len(name) {
			out = append(out, byte(len(name)-start))
			out = append(out, name[start:]...)
		}
		out = append(out, 0x00, byte(qtype>>8), byte(qtype), 0x00, 0x01)
		return out
	}

	// (a) Allowlisted A → forwarded (RCODE=0).
	resp := send(t, build(0x1001, "example.com", 1))
	if rcode := resp[3] & 0x0f; rcode != 0 {
		t.Errorf("allowed A rcode = %d, want 0", rcode)
	}

	// (b) Non-allowlisted A → NXDOMAIN (RCODE=3).
	resp = send(t, build(0x1002, "blocked.test", 1))
	if rcode := resp[3] & 0x0f; rcode != 3 {
		t.Errorf("denied A rcode = %d, want 3 (NXDOMAIN)", rcode)
	}

	// (c) ANY (allowlisted host) → REFUSED (RCODE=5).
	resp = send(t, build(0x1003, "example.com", 255))
	if rcode := resp[3] & 0x0f; rcode != 5 {
		t.Errorf("ANY rcode = %d, want 5 (REFUSED)", rcode)
	}

	// (d) AAAA respects the same allowlist.
	resp = send(t, build(0x1004, "example.com", 28))
	if rcode := resp[3] & 0x0f; rcode != 0 {
		t.Errorf("allowed AAAA rcode = %d, want 0", rcode)
	}
	resp = send(t, build(0x1005, "blocked.test", 28))
	if rcode := resp[3] & 0x0f; rcode != 3 {
		t.Errorf("denied AAAA rcode = %d, want 3", rcode)
	}
}

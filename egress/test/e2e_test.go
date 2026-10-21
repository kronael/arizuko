// Package test exercises the egress proxy end-to-end over real sockets:
// a real listener, a real HTTP client with HTTP_PROXY set, real upstreams.
// It models the 9-outcome matrix that openclaw-managed-agents'
// test/e2e-networking.sh proves at the docker network layer. The cases that
// fundamentally require kernel-level isolation (raw TCP bypass, IMDS bypass)
// are skipped here with notes pointing at the missing infra — the proxy owns
// only the proxy half of the enforcement; the other half is the sandbox's
// internal network, which has no route out except through this proxy.
//
// The allowlist is fixed at construction, so there is no registry. There is
// no DNS filter case: a single-tenant proxy already sees every destination in
// the CONNECT line, and a rule expressed twice can disagree with itself
// (spec 6/24).
package test

import (
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

	"github.com/kronael/arizuko/egress"
)

// envFixture stands up one proxy plus loopback upstreams per case.
// Upstreams listen on 127.0.0.1; test URLs use the "localhost" hostname so
// the allowlist (host-name based) must match a NAME — an IP entry is refused
// at construction, so a test cannot allowlist "127.0.0.1".
type envFixture struct {
	t            *testing.T
	proxyURL     *url.URL
	httpUpstream *httptest.Server
	tlsUpstream  *httptest.Server
}

func newEnv(t *testing.T, allowlist []string) *envFixture {
	t.Helper()

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

	// client "" — the peer pin is proved in the package's own tests; these
	// cases are about the allowlist.
	p, err := egress.New(allowlist, nil, "")
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	// The upstreams are httptest servers on 127.0.0.1, so this harness is the
	// local-development case the option exists for: the destination genuinely
	// is this host. Everything else about the proxy stays default.
	p.SetAllowLoopback(true)
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
		proxyURL:     purl,
		httpUpstream: httpUpstream,
		tlsUpstream:  tlsUpstream,
	}
}

// clientWithProxy returns an http.Client that routes all traffic through
// the fixture's proxy. InsecureSkipVerify covers the fact that the upstream
// test cert is for 127.0.0.1, not localhost — TLS verification is not what
// these cases are about; the proxy's allowlist enforcement is.
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
		},
	}
}

// asLocalhost rewrites an httptest server URL ("http://127.0.0.1:NNNN/")
// into "<scheme>://localhost:NNNN/" so the proxy sees a name.
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
	// CONNECT denial surfaces as a proxy 4xx through the transport: the TLS
	// handshake never starts. Either err != nil or status >= 400 is a pass;
	// a 200 is the failure.
	if err == nil && resp.StatusCode == http.StatusOK {
		t.Fatalf("denied HTTPS reached upstream (status=%d)", resp.StatusCode)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// ---- case 5: raw TCP socket.connect to allowlisted host:port -----------

// This proxy speaks HTTP/CONNECT. Raw TCP cannot transit it at all — it is
// not a SOCKS daemon, and with transparent mode deleted (spec 6/24) there
// is no interception path either. In production the sandbox sits on a
// docker --internal network: raw TCP has nowhere to go.
func TestE2E_Case5_RawTCPAllowed(t *testing.T) {
	t.Skip("no transparent mode by design (6/24): raw TCP has no path through " +
		"a forward proxy, and the sandbox's internal network gives it no path around")
}

// ---- case 6: raw TCP socket.connect to non-allowlisted IP --------------

func TestE2E_Case6_RawTCPDenied(t *testing.T) {
	t.Skip("requires Docker --internal network or netns isolation; the mitigation " +
		"lives in the container runtime, not in the proxy. Asserted by " +
		"openclaw test/e2e-networking.sh case 3 on a real docker daemon")
}

// ---- case 7: AWS IMDS via proxy (169.254.169.254) ----------------------

func TestE2E_Case7_IMDSViaProxy(t *testing.T) {
	// The allowlist does NOT include 169.254.169.254 — IMDS is the canonical
	// SSRF pivot and must be denied at the proxy layer.
	env := newEnv(t, []string{"localhost"})
	c := env.clientWithProxy()
	// Short timeout so this never hangs if a bug dials IMDS for real.
	c.Timeout = 2 * time.Second

	resp, err := c.Get("http://169.254.169.254/latest/meta-data/")
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

func TestE2E_Case8_IMDSDirect(t *testing.T) {
	t.Skip("requires netns / Docker --internal isolation; the proxy alone " +
		"cannot enforce. Asserted by openclaw test/e2e-networking.sh case 4b")
}

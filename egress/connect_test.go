package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The CONNECT path is how every HTTPS request from a sandbox leaves. It had no
// test at all: handleConnect, splice and serveHealth were 0% covered, so the
// tunnel's allow decision, its address recheck and its byte plumbing were
// unexercised while the plain-HTTP path beside them was well covered.

// echoServer accepts one connection and echoes bytes back, standing in for the
// TLS origin a real CONNECT reaches. Nothing decrypts, so raw bytes prove the
// tunnel exactly as well as a handshake would.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return lis
}

// connectVia opens a raw connection to the proxy and sends a CONNECT line the
// way an HTTPS client does. Returning the live connection is the point: the
// tunnel is what is under test, not the status line alone.
func connectVia(t *testing.T, proxy *url.URL, authority string) (net.Conn, *http.Response) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	// Only a 200 opens a tunnel. On a refusal the proxy writes an error body,
	// which is the buffered bytes; on a 200 anything buffered would be tunnel
	// payload this helper is about to drop, so that case is a failure.
	if resp.StatusCode == http.StatusOK && br.Buffered() > 0 {
		t.Fatalf("%d bytes buffered after 200 Connection Established; the tunnel would lose them", br.Buffered())
	}
	return c, resp
}

func TestConnectTunnelsBytesBothWaysForAnAllowlistedHost(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	proxy := serve(t, mustNew(t, []string{"localhost"}, nil, ""))

	conn, resp := connectVia(t, proxy, "localhost:"+port)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT to an allowlisted host = %s, want 200", resp.Status)
	}

	// Client to origin.
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write into the tunnel: %v", err)
	}
	// Origin back to client — the second half of splice.
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read back through the tunnel: %v", err)
	}
	if got != "ping\n" {
		t.Errorf("tunnel returned %q, want the echoed %q", got, "ping\n")
	}
}

// The allow half must not be the only thing that works. A CONNECT to a host
// that is not on the list is the request this proxy exists to stop.
func TestConnectRefusesAHostThatIsNotAllowlisted(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	proxy := serve(t, mustNew(t, []string{"example.com"}, nil, ""))

	_, resp := connectVia(t, proxy, "localhost:"+port)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT to a host off the list = %s, want 403", resp.Status)
	}
}

// serveStrict is serve without its SetAllowLoopback(true). Every other test in
// this package runs against a loopback upstream and needs that allowance; the
// address check itself must be observed with it off.
func serveStrict(t *testing.T, p *Proxy) *url.URL {
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

// The tunnel dials the ADDRESS the check resolved, not the name again. Proving
// that needs a target the proxy could reach if it dialled the name: an
// allowlisted host, live and listening, whose address the policy refuses.
// Swapping dialAllowed for a plain net.Dial makes this test the only one that
// notices — every other CONNECT case names a host that does not resolve, so a
// bypassed check still fails on the dial.
func TestConnectRefusesAReachableHostWhoseAddressIsBlocked(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo.Addr().String())

	// localhost is on the allowlist and is listening. 127.0.0.1 is blocked
	// because SetAllowLoopback was never called.
	proxy := serveStrict(t, mustNew(t, []string{"localhost"}, nil, ""))

	_, resp := connectVia(t, proxy, "localhost:"+port)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a live allowlisted host on a blocked address was tunnelled: %s", resp.Status)
	}
}

// The name check and the address check are two decisions, and the tunnel runs
// both. An allowlisted name whose answer is a blocked class — the rebinding
// case address.go exists for — must be refused after the name passes.
func TestConnectRefusesAnAllowlistedNameResolvingToABlockedAddress(t *testing.T) {
	p := mustNew(t, []string{"rebound.example"}, nil, "")
	p.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	proxy := serve(t, p)

	_, resp := connectVia(t, proxy, "rebound.example:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a name resolving to instance metadata was tunnelled: %s", resp.Status)
	}
}

// allow-all opens NAMES. It must not open the blocked address classes with
// them, or every allow-all folder gets a route to the instance metadata
// service.
func TestConnectAllowAllStillRefusesABlockedAddress(t *testing.T) {
	p := mustNew(t, nil, nil, "")
	p.SetAllowAll(true)
	p.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	proxy := serve(t, p)

	_, resp := connectVia(t, proxy, "anything.example:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("allow-all tunnelled to instance metadata: %s", resp.Status)
	}
}

// The peer pin is checked before the method split, so it must cover CONNECT
// too — not only the plain-HTTP path the existing tests exercise.
func TestConnectRefusesAForeignPeer(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	proxy := serve(t, mustNew(t, []string{"localhost"}, nil, "10.99.0.7"))

	_, resp := connectVia(t, proxy, "localhost:"+port)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a peer that is not the pinned client opened a tunnel: %s", resp.Status)
	}
}

// An allowlisted host that does not answer is not a refusal. The agent behind
// this proxy reads the status: a 403 says "ask for a grant", so a dead upstream
// answered with 403 sends it asking for a grant it already holds.
func TestConnectAnswers502WhenAnAllowlistedHostDoesNotAnswer(t *testing.T) {
	closed := echoServer(t)
	_, port, _ := net.SplitHostPort(closed.Addr().String())
	_ = closed.Close()
	proxy := serve(t, mustNew(t, []string{"localhost"}, nil, ""))

	_, resp := connectVia(t, proxy, "localhost:"+port)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT to an allowlisted host that refuses the dial = %s, want 502", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "upstream unreachable: ") {
		t.Errorf("body = %q, want the dial error behind an 'upstream unreachable' prefix", body)
	}
}

func TestConnectAnswers502WhenAnAllowlistedNameDoesNotResolve(t *testing.T) {
	p := mustNew(t, []string{"nowhere.example"}, nil, "")
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "nowhere.example", IsNotFound: true}
	}
	proxy := serve(t, p)

	_, resp := connectVia(t, proxy, "nowhere.example:443")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT to an allowlisted name that does not resolve = %s, want 502", resp.Status)
	}
}

// The plain-HTTP forward classifies the same way: a blocked address is the
// proxy refusing, not the upstream failing.
func TestHTTPRefusesABlockedAddressWith403(t *testing.T) {
	p := mustNew(t, []string{"rebound.example"}, nil, "")
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	proxy := serve(t, p)

	resp, err := through(proxy).Get("http://rebound.example/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET through a name resolving to instance metadata = %s, want 403", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "blocked address") {
		t.Errorf("body = %q, want the blocked-address reason", body)
	}
}

// A client that omits the port means 443. hostPort covers the normalisation in
// isolation; this proves the tunnel actually reaches the default port.
func TestConnectWithNoPortDialsFourFourThree(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:443")
	if err != nil {
		t.Skip("cannot bind 127.0.0.1:443 in this environment")
	}
	defer lis.Close()
	go func() {
		c, err := lis.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()

	proxy := serve(t, mustNew(t, []string{"localhost"}, nil, ""))
	conn, resp := connectVia(t, proxy, "localhost")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("portless CONNECT = %s, want 200", resp.Status)
	}
	if _, err := conn.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := bufio.NewReader(conn).ReadString('\n'); got != "hi\n" {
		t.Errorf("tunnel to the default port returned %q", got)
	}
}

// The health probe.

func healthCheck(t *testing.T, proxy *url.URL) *http.Response {
	t.Helper()
	// Origin-form GET straight at the proxy's own listener, not through it —
	// this is a probe of the process, which is why ServeHTTP answers it before
	// the peer check.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(proxy.String() + HealthPath)
	if err != nil {
		t.Fatalf("health probe: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// The readiness contract: a 200 means listening AND filtering. Answering 200
// before the listener is up would let runed release a spawn into a proxy that
// is not yet enforcing.
func TestHealthIs503UntilSetReady(t *testing.T) {
	p := mustNew(t, []string{"localhost"}, nil, "")
	proxy := serve(t, p)

	if resp := healthCheck(t, proxy); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("health before SetReady = %s, want 503", resp.Status)
	}
	p.SetReady(true)
	resp := healthCheck(t, proxy)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health after SetReady = %s, want 200", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok"`) {
		t.Errorf("health body = %q, want a status:ok document", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("health Content-Type = %q, want application/json", ct)
	}
}

// "Deny everything" is a rule a folder can legitimately have. 503-ing on an
// empty allowlist would fail every such spawn's readiness probe.
func TestHealthIsReadyWithAnEmptyAllowlist(t *testing.T) {
	p := mustNew(t, nil, nil, "")
	p.SetReady(true)
	if resp := healthCheck(t, serve(t, p)); resp.StatusCode != http.StatusOK {
		t.Errorf("health with an empty allowlist = %s, want 200", resp.Status)
	}
}

// The probe comes from runed, not from the pinned spawn, so it must be
// answered before the peer check rejects it.
func TestHealthAnswersAPeerThatIsNotThePinnedClient(t *testing.T) {
	p := mustNew(t, []string{"localhost"}, nil, "10.99.0.7")
	p.SetReady(true)
	if resp := healthCheck(t, serve(t, p)); resp.StatusCode != http.StatusOK {
		t.Errorf("health from an unpinned peer = %s, want 200", resp.Status)
	}
}

// Hop-by-hop headers.

func TestIsHopByHopCoversTheRFCSetCaseInsensitively(t *testing.T) {
	for _, h := range []string{
		"Connection", "connection", "KEEP-ALIVE", "Proxy-Authenticate",
		"Proxy-Authorization", "Proxy-Connection", "TE", "Trailers",
		"Transfer-Encoding", "Upgrade",
	} {
		if !isHopByHop(h) {
			t.Errorf("isHopByHop(%q) = false; forwarding it corrupts the next hop", h)
		}
	}
	for _, h := range []string{"Authorization", "Content-Type", "User-Agent", "Cookie", "Host"} {
		if isHopByHop(h) {
			t.Errorf("isHopByHop(%q) = true; the request would lose an end-to-end header", h)
		}
	}
}

// A hop-by-hop header belongs to one connection. Forwarding the client's
// Connection header, or the origin's Transfer-Encoding, breaks framing on the
// other side; end-to-end headers must survive in both directions.
func TestHandleHTTPStripsHopByHopBothWays(t *testing.T) {
	var sawConnection, sawAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawConnection = r.Header.Get("Proxy-Authorization")
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Proxy-Authenticate", "Basic realm=x")
		w.Header().Set("X-Origin-Header", "kept")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "body")
	}))
	t.Cleanup(up.Close)

	c := through(serve(t, mustNew(t, []string{"localhost"}, nil, "")))
	req, err := http.NewRequest("GET", asLocalhost(up.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("Authorization", "Bearer end-to-end")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer resp.Body.Close()

	if sawConnection != "" {
		t.Errorf("Proxy-Authorization reached the origin: %q", sawConnection)
	}
	if sawAuth != "Bearer end-to-end" {
		t.Errorf("Authorization did not survive the hop: %q", sawAuth)
	}
	if got := resp.Header.Get("Proxy-Authenticate"); got != "" {
		t.Errorf("the origin's Proxy-Authenticate was relayed to the client: %q", got)
	}
	if got := resp.Header.Get("X-Origin-Header"); got != "kept" {
		t.Errorf("an end-to-end response header was dropped: %q", got)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %s, want the origin's 418", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "body" {
		t.Errorf("body = %q, want the origin's", body)
	}
}

// splice, on its own.

// splice returns as soon as EITHER direction ends, so a tunnel whose origin
// hangs up does not leak a goroutine pair per spawn.
func TestSpliceReturnsWhenOneSideCloses(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	done := make(chan struct{})
	go func() { splice(a1, b1); close(done) }()

	if _, err := a2.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := b2.Read(buf); err != nil || buf[0] != 'x' {
		t.Fatalf("splice did not copy a->b: %v %q", err, buf)
	}

	_ = b2.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("splice did not return after one side closed; every tunnel would leak two goroutines")
	}
	_ = a2.Close()
}

// resolveDest error paths.

// A name that does not resolve must be a refusal, never a fall-through to a
// dial. The proxy is the one process on the spawn's path with an uplink, so
// "could not check" has to mean "not going".
func TestResolveDestRefusesAnUnresolvableName(t *testing.T) {
	p := mustNew(t, []string{"nowhere.example"}, nil, "")
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return nil, fmt.Errorf("NXDOMAIN")
	}
	if _, err := p.resolveDest(context.Background(), "nowhere.example"); err == nil {
		t.Fatal("an unresolvable name was accepted as a destination")
	}

	p.lookup = func(context.Context, string) ([]net.IP, error) { return nil, nil }
	if _, err := p.resolveDest(context.Background(), "nowhere.example"); err == nil {
		t.Fatal("a name resolving to zero addresses was accepted as a destination")
	}
}

// A CONNECT authority can be a literal address, which never reaches a
// resolver. The address checks must still run on it — this is the last check
// before a socket.
func TestResolveDestChecksALiteralAddress(t *testing.T) {
	p := mustNew(t, nil, nil, "")
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		t.Fatal("a literal address must not reach the resolver")
		return nil, nil
	}

	if _, err := p.resolveDest(context.Background(), "169.254.169.254"); err == nil {
		t.Error("the instance metadata address was accepted as a literal")
	}
	if _, err := p.resolveDest(context.Background(), "127.0.0.1"); err == nil {
		t.Error("loopback was accepted without SetAllowLoopback")
	}
	got, err := p.resolveDest(context.Background(), "93.184.216.34")
	if err != nil {
		t.Fatalf("a public literal was refused: %v", err)
	}
	if !got.Equal(net.ParseIP("93.184.216.34")) {
		t.Errorf("resolveDest returned %s, want the literal it was given", got)
	}
}

func TestBlockedIPCoversTheClassesTheProxyMustNeverReach(t *testing.T) {
	p := mustNew(t, nil, nil, "")
	blocked := map[string]string{
		"a nil address":             "",
		"loopback":                  "127.0.0.1",
		"IPv6 loopback":             "::1",
		"link-local (cloud IMDS)":   "169.254.169.254",
		"IPv6 link-local":           "fe80::1",
		"the unspecified address":   "0.0.0.0",
		"multicast":                 "224.0.0.1",
		"interface-local multicast": "ff01::1",
		"AWS IMDSv2 over IPv6":      "fd00:ec2::254",
	}
	for name, raw := range blocked {
		var ip net.IP
		if raw != "" {
			ip = net.ParseIP(raw)
			if ip == nil {
				t.Fatalf("%s: unparseable fixture %q", name, raw)
			}
		}
		if !p.blockedIP(ip) {
			t.Errorf("blockedIP(%s / %q) = false", name, raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if p.blockedIP(net.ParseIP(raw)) {
			t.Errorf("blockedIP(%q) = true; an ordinary public destination was refused", raw)
		}
	}
	// Loopback is the one class an option unblocks.
	p.SetAllowLoopback(true)
	if p.blockedIP(net.ParseIP("127.0.0.1")) {
		t.Error("SetAllowLoopback did not unblock loopback")
	}
	if !p.blockedIP(net.ParseIP("169.254.169.254")) {
		t.Error("SetAllowLoopback unblocked link-local as well; it must open loopback only")
	}
}

// BUGS L109: the plain-HTTP path follows redirects through a client whose only
// per-hop check is the ADDRESS dialler. hostAllowed runs once, on the original
// request, so an allowlisted host that answers 302 sends the proxy to any
// public host it names. Reproduced against a binary built from HEAD.
//
// Left skipped rather than deleted: the entry is a proposal awaiting sign-off,
// and this is the assertion the fix has to satisfy. Un-skip with the fix.
func TestHTTPRedirectToAnUnallowlistedHostIsRefused(t *testing.T) {
	t.Skip("BUGS L109 — open: hostAllowed is not re-checked on a redirect hop")

	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "exfiltrated")
	}))
	t.Cleanup(sink.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	// Only the redirector is allowlisted. The sink is not.
	c := through(serve(t, mustNew(t, []string{"localhost"}, nil, "")))
	resp, err := c.Get(asLocalhost(redirector.URL))
	if err != nil {
		return // a transport-level refusal is a correct outcome too
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "exfiltrated") {
		t.Fatal("a redirect reached a host that is not on the allowlist")
	}
}

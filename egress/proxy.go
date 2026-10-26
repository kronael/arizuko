// Package egress is a forward HTTP / CONNECT-tunnel proxy with one static
// allowlist and at most one client. It is the whole enforcement point: the
// destination comes from the request line the client itself sends, so there
// is no TLS termination, no CA, no SNI peek and no interception.
//
// One proxy serves one sandbox. There is no registry and no admin API —
// the allowlist is fixed when the process starts (spec 6/24).
package egress

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/egress/match"
)

// HealthPath is the readiness probe. It is public and unauthenticated, like
// every other arizuko daemon's, and it is what the caller polls before it
// starts the sandbox: 200 means this proxy is serving with its allowlist
// loaded, so the sandbox's first request cannot arrive before enforcement.
const HealthPath = "/health"

const (
	dialTimeout    = 10 * time.Second
	httpReadHeader = 5 * time.Second
)

// hostPort normalizes a CONNECT authority to host:port. A CONNECT line
// carries the port, but a client that omits it means 443 — the port only
// selects the socket; the allowlist and the address check already ran.
func hostPort(authority string) string {
	if _, _, err := net.SplitHostPort(authority); err == nil {
		return authority
	}
	return net.JoinHostPort(authority, "443")
}

// Proxy is one sandbox's egress path.
type Proxy struct {
	// allow and deny are the fixed rule lists, decided by match.Allowed —
	// deny FIRST, so a deny entry carves an exception out of a broad allow
	// ("all of example.com except internal.example.com"). Both are validated
	// by New: a rule that can never match is refused at construction, not
	// silently carried (spec 6/25 1b).
	allow []string
	deny  []string
	// allowAll permits every host NAME — the explicit mode that replaced the
	// magic "*" list entry. The deny list and the address checks still apply:
	// allow-all opens names, not blocked address classes.
	allowAll bool
	// client is the only peer address allowed to use this proxy. Empty
	// accepts any peer.
	//
	// A per-spawn proxy shares its network with the other spawns of the
	// same folder, and it also sits on an uplink network with the daemons.
	// Without this check a neighbour could send its CONNECT here and borrow
	// an allowlist it was never granted (an elevated turn's is unrestricted).
	// This is a bind scope for a single-tenant listener, not an identity
	// lookup: nothing is registered, nothing is looked up, and a second
	// client is refused rather than served from a different rule set.
	client string
	// denyPrivate additionally refuses destinations that resolve into a
	// private range. Off by default: a per-spawn proxy shares the compose
	// network with the daemons, so private is normal here (see address.go).
	denyPrivate bool
	// allowLoopback permits destinations on this host. Off by default — a
	// spawn's proxy has no business dialling its own container. On for local
	// development, where the destination genuinely IS this machine (a service
	// on the developer's laptop, an httptest upstream).
	allowLoopback bool
	// lookup resolves a name. A field so a test can answer without DNS: the
	// policy worth testing here is what happens for a given ANSWER, and a real
	// resolver cannot be made to return the metadata address on demand.
	lookup func(ctx context.Context, host string) ([]net.IP, error)
	// transport forwards plain HTTP. It carries the same dialAllowed as the
	// CONNECT path so both reach the address that passed the check, not the
	// name that passed it.
	transport *http.Transport
	// ready gates HealthPath. The binary sets it once the listener is bound;
	// the allowlist is already fixed by then (New takes it), so a 200 means
	// listening AND enforcing — never listening-but-not-yet-filtering.
	ready atomic.Bool
}

// SetReady flips the health probe to 200. Call it after the listener binds.
func (p *Proxy) SetReady(ready bool) { p.ready.Store(ready) }

// SetDenyPrivate refuses destinations resolving into a private range.
func (p *Proxy) SetDenyPrivate(deny bool) { p.denyPrivate = deny }

// SetAllowLoopback permits destinations on this host — local development only.
func (p *Proxy) SetAllowLoopback(allow bool) { p.allowLoopback = allow }

// SetAllowAll permits every host name; deny and the address checks still apply.
func (p *Proxy) SetAllowAll(allow bool) { p.allowAll = allow }

// New returns a proxy that allows exactly allow minus deny, for exactly
// client (an IP address; empty accepts any peer). Both lists are validated:
// an entry that can never match — a URL, a host:port, an IP, a stray
// wildcard — is refused here, where the list is accepted, instead of
// reading as protection at match time.
func New(allow, deny []string, client string) (*Proxy, error) {
	if err := match.ValidateLists(allow, deny); err != nil {
		return nil, err
	}
	p := &Proxy{allow: allow, deny: deny, client: client}
	p.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	p.transport = &http.Transport{
		DialContext:           p.dialAllowed,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
	return p, nil
}

// hostAllowed is the one name-policy decision for both the CONNECT tunnel
// and the plain-HTTP forward: deny first, then allow-all, then the allowlist.
func (p *Proxy) hostAllowed(host string) bool {
	return match.Allowed(p.allow, p.deny, p.allowAll, host)
}

// httpClient forwards plain-HTTP requests through this proxy's checked dialer.
// It never follows a redirect: the 3xx goes back to the client, which re-issues
// the request so the new host passes hostAllowed. Following one here would move
// the destination decision out of the request line this proxy enforces on.
func (p *Proxy) httpClient() *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: p.transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Proxy) Server(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           p,
		ReadHeaderTimeout: httpReadHeader,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A forward proxy is addressed in absolute form ("GET http://host/p"), so
	// an origin-form GET is not proxy traffic — it is a probe of this process.
	// Answer it before the peer check: the caller polling for readiness is not
	// the pinned client, and proxying it would make us dial ourselves.
	if r.Method == http.MethodGet && r.URL.Host == "" && r.URL.Path == HealthPath {
		p.serveHealth(w)
		return
	}

	src, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !p.peerAllowed(src) {
		slog.Warn("deny peer", "src", src, "client", p.client)
		http.Error(w, "denied: peer", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		p.handleConnect(w, r, src)
		return
	}
	p.handleHTTP(w, r, src)
}

// serveHealth reports whether this proxy is serving with its allowlist
// loaded. An EMPTY allowlist is ready: "deny everything" is a rule a folder
// can legitimately have, and 503-ing on it would fail every such spawn.
func (p *Proxy) serveHealth(w http.ResponseWriter) {
	if !p.ready.Load() {
		http.Error(w, `{"status":"starting"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (p *Proxy) peerAllowed(src string) bool {
	if p.client == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(src), []byte(p.client)) == 1
}

// handleConnect tunnels HTTPS. The destination is r.Host — the
// `CONNECT host:443` line the client sends because HTTP_PROXY points here.
// Nothing is decrypted; the two sockets are spliced once the host matches.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request, src string) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if !p.hostAllowed(host) {
		slog.Info("deny connect", "src", src, "host", host)
		http.Error(w, "denied: host not allowed", http.StatusForbidden)
		return
	}

	// Dial the ADDRESS the check resolved, never the name again: dialling the
	// name here would resolve a second time and the answer that passed would
	// not be the answer reached (spec 6/25 decision 1).
	upstream, err := p.dialAllowed(r.Context(), "tcp", hostPort(r.Host))
	if err != nil {
		writeDialError(w, src, host, err)
		return
	}
	defer func() { _ = upstream.Close() }()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	slog.Info("allow connect", "src", src, "host", host)
	splice(client, upstream)
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request, src string) {
	host := r.Host
	if r.URL != nil && r.URL.Host != "" {
		host = r.URL.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	if !p.hostAllowed(host) {
		slog.Info("deny http", "src", src, "host", host)
		http.Error(w, "denied: host not allowed", http.StatusForbidden)
		return
	}

	target := r.URL.String()
	if !strings.HasPrefix(target, "http") {
		target = "http://" + r.Host + r.URL.RequestURI()
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for k, vv := range r.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			out.Header.Add(k, v)
		}
	}
	resp, err := p.httpClient().Do(out)
	if err != nil {
		writeDialError(w, src, host, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	slog.Info("allow http", "src", src, "host", host, "status", resp.StatusCode)
}

// writeDialError answers a request whose name passed but whose dial did not.
// A blocked address is this proxy refusing, so it stays a 403; a name that
// did not resolve or an upstream that did not answer is past the policy and
// is a 502, so the client can tell "not allowed" from "allowed, unreachable".
func writeDialError(w http.ResponseWriter, src, host string, err error) {
	if errors.Is(err, errBlockedAddress) {
		slog.Info("deny address", "src", src, "host", host, "err", err)
		http.Error(w, "denied: "+err.Error(), http.StatusForbidden)
		return
	}
	slog.Info("fail upstream", "src", src, "host", host, "err", err)
	http.Error(w, "upstream unreachable: "+err.Error(), http.StatusBadGateway)
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"proxy-connection", "te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

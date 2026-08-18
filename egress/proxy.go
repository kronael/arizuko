// Package egress is a forward HTTP / CONNECT-tunnel proxy with one static
// allowlist and at most one client. It is the whole enforcement point: the
// destination comes from the request line the client itself sends, so there
// is no TLS termination, no CA, no SNI peek and no interception.
//
// One proxy serves one sandbox. There is no registry and no admin API —
// the allowlist is fixed when the process starts (spec 6/24).
package egress

import (
	"crypto/subtle"
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

// defaultClient bounds upstream behavior: per-call total deadline, header
// timeout, idle pool. Without this an unresponsive upstream pegs goroutines
// and sockets indefinitely.
var defaultClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	},
}

// Proxy is one sandbox's egress path.
type Proxy struct {
	// allow is the fixed allowlist, matched by match.Host.
	allow []string
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
	// ready gates HealthPath. The binary sets it once the listener is bound;
	// the allowlist is already fixed by then (New takes it), so a 200 means
	// listening AND enforcing — never listening-but-not-yet-filtering.
	ready atomic.Bool
}

// SetReady flips the health probe to 200. Call it after the listener binds.
func (p *Proxy) SetReady(ready bool) { p.ready.Store(ready) }

// New returns a proxy that allows exactly allow, for exactly client.
// client is an IP address; empty accepts any peer.
func New(allow []string, client string) *Proxy {
	return &Proxy{allow: allow, client: client}
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
		http.Error(w, "denied", http.StatusForbidden)
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
	if !match.Host(p.allow, host) {
		slog.Info("deny connect", "src", src, "host", host)
		http.Error(w, "denied", http.StatusForbidden)
		return
	}

	upstream, err := net.DialTimeout("tcp", r.Host, dialTimeout)
	if err != nil {
		slog.Info("upstream dial", "host", r.Host, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

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
	defer client.Close()
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

	if !match.Host(p.allow, host) {
		slog.Info("deny http", "src", src, "host", host)
		http.Error(w, "denied", http.StatusForbidden)
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
	resp, err := defaultClient.Do(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	slog.Info("allow http", "src", src, "host", host, "status", resp.StatusCode)
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

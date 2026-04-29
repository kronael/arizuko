// Package proxy implements the forward HTTP / CONNECT-tunnel HTTPS proxy
// that gates traffic by per-source-IP allowlist. Lookups go directly to
// admin.Registry — there is one registry, no need for an interface.
package proxy

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/crackbox/pkg/admin"
)

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

type Proxy struct {
	allow *admin.Registry
}

func New(allow *admin.Registry) *Proxy {
	return &Proxy{allow: allow}
}

func (p *Proxy) Server(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           p,
		ReadHeaderTimeout: httpReadHeader,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src, _, _ := net.SplitHostPort(r.RemoteAddr)

	if r.Method == http.MethodConnect {
		p.handleConnect(w, r, src)
		return
	}
	p.handleHTTP(w, r, src)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request, src string) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	id, ok := p.allow.Allow(src, host)
	if !ok {
		slog.Info("deny connect", "src", src, "id", id, "host", host)
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
	slog.Info("allow connect", "src", src, "id", id, "host", host)
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

	id, ok := p.allow.Allow(src, host)
	if !ok {
		slog.Info("deny http", "src", src, "id", id, "host", host)
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
	slog.Info("allow http", "src", src, "id", id, "host", host, "status", resp.StatusCode)
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

package main

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	dialTimeout    = 10 * time.Second
	httpReadHeader = 5 * time.Second
)

// Proxy is a forward HTTP/HTTPS proxy. Agent containers are configured with
// HTTPS_PROXY=http://egred:3128 (transparent path was rejected — Docker
// bridges put the host on the gateway, not egred). For HTTP requests we
// forward and check the Host header. For HTTPS, we honor CONNECT and check
// the host:port target — no MITM, no TLS termination.
type Proxy struct {
	allow *Allowlist
}

func NewProxy(allow *Allowlist) *Proxy {
	return &Proxy{allow: allow}
}

func (p *Proxy) Server() *http.Server {
	return &http.Server{
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

// handleConnect tunnels HTTPS opaquely once the host passes the allowlist.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request, src string) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	folder, ok := p.allow.Allow(src, host)
	if !ok {
		slog.Info("deny connect", "src", src, "folder", folder, "host", host)
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
	slog.Info("allow connect", "src", src, "folder", folder, "host", host)
	splice(client, upstream)
}

// handleHTTP forwards plain-HTTP requests after host allowlist check.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request, src string) {
	host := r.Host
	if r.URL != nil && r.URL.Host != "" {
		host = r.URL.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	folder, ok := p.allow.Allow(src, host)
	if !ok {
		slog.Info("deny http", "src", src, "folder", folder, "host", host)
		http.Error(w, "denied", http.StatusForbidden)
		return
	}

	target := r.URL.String()
	if !strings.HasPrefix(target, "http") {
		target = "http://" + r.Host + r.URL.RequestURI()
	}
	out, err := http.NewRequest(r.Method, target, r.Body)
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
	resp, err := http.DefaultClient.Do(out)
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
	slog.Info("allow http", "src", src, "folder", folder, "host", host, "status", resp.StatusCode)
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
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

// silence imports during the rewrite (peek/origdst no longer used here).
var _ = bufio.NewReader
var _ = errors.New

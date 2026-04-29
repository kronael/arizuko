package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	peekDeadline = 10 * time.Second
	dialTimeout  = 10 * time.Second
	httpPeekMax  = 8192
)

// Proxy accepts intercepted connections (via iptables REDIRECT), peeks the
// hostname (SNI for :443, Host header for :80), checks the per-source-IP
// allowlist, and splices to the original destination.
type Proxy struct {
	allow *Allowlist
	wg    sync.WaitGroup
}

func NewProxy(allow *Allowlist) *Proxy {
	return &Proxy{allow: allow}
}

func (p *Proxy) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			slog.Warn("accept", "err", err)
			continue
		}
		p.wg.Add(1)
		go func(c net.Conn) {
			defer p.wg.Done()
			p.handle(c)
		}(c)
	}
}

func (p *Proxy) Wait() { p.wg.Wait() }

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		slog.Warn("non-tcp conn", "remote", c.RemoteAddr())
		return
	}
	srcIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	dst, err := originalDst(tcp)
	if err != nil {
		slog.Warn("orig dst", "src", srcIP, "err", err)
		return
	}
	dstHost, dstPort, _ := net.SplitHostPort(dst)
	port, _ := strconv.Atoi(dstPort)

	pc := newPeekedConn(c)

	host, err := peekHost(pc, port)
	if err != nil {
		slog.Info("peek host", "src", srcIP, "dst", dst, "err", err)
		return
	}

	folder, allowed := p.allow.Allow(srcIP, host)
	if !allowed {
		slog.Info("deny",
			"src", srcIP, "folder", folder,
			"host", host, "dst", dst, "port", port)
		return
	}
	slog.Info("allow",
		"src", srcIP, "folder", folder,
		"host", host, "dst", dst)

	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(dstHost, dstPort), dialTimeout)
	if err != nil {
		slog.Info("upstream dial", "host", host, "dst", dst, "err", err)
		return
	}
	defer upstream.Close()
	splice(pc, upstream)
}

func peekHost(c *peekedConn, port int) (string, error) {
	switch port {
	case 443:
		return peekTLSHostname(c, peekDeadline)
	case 80:
		return peekHTTPHost(c, httpPeekMax, peekDeadline)
	default:
		return "", errors.New("only :80 and :443 supported")
	}
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

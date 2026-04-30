// Transparent-mode listener: accepts iptables-REDIRECT'd traffic, reads
// the pre-NAT destination via SO_ORIGINAL_DST, peeks SNI (port 443) or
// HTTP Host (port 80), runs the same per-source-IP allowlist as the
// forward proxy, then splices to upstream.
//
// The listener is harmless when nothing redirects to it — it's idle.
// Binding it by default costs nothing operationally; users who want it
// point their iptables at it.

package proxy

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"time"
)

const (
	transparentPeekDeadline = 10 * time.Second
	transparentMaxHTTPPeek  = 8 * 1024
)

// origDst is the indirection used to read SO_ORIGINAL_DST. Tests override
// it; production code uses the linux/non-linux build-tagged originalDst.
var origDst = originalDst

// ServeTransparent runs the transparent-mode loop on l until l is closed.
// Each accepted connection is handled in its own goroutine.
func (p *Proxy) ServeTransparent(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handleTransparent(c)
	}
}

func (p *Proxy) handleTransparent(c net.Conn) {
	defer c.Close()

	src, _, _ := net.SplitHostPort(c.RemoteAddr().String())

	tcp, ok := c.(*net.TCPConn)
	if !ok {
		slog.Info("transparent non-tcp", "src", src, "type", "unknown")
		return
	}
	dst, err := origDst(tcp)
	if err != nil {
		slog.Info("transparent orig dst", "src", src, "err", err)
		return
	}
	_, dstPort, err := net.SplitHostPort(dst)
	if err != nil {
		slog.Info("transparent split dst", "src", src, "dst", dst, "err", err)
		return
	}

	pc := newPeekedConn(c)
	var host string
	switch dstPort {
	case "443":
		host, err = peekTLSHostname(pc, transparentPeekDeadline)
	case "80":
		host, err = peekHTTPHost(pc, transparentMaxHTTPPeek, transparentPeekDeadline)
	default:
		slog.Info("transparent unsupported port", "src", src, "dst", dst)
		return
	}
	if err != nil {
		slog.Info("transparent peek", "src", src, "dst", dst, "err", err)
		return
	}

	id, allowed := p.allow.Allow(src, host)
	if !allowed {
		slog.Info("deny transparent", "src", src, "id", id, "host", host, "dst", dst)
		return
	}

	upstream, err := net.DialTimeout("tcp", dst, dialTimeout)
	if err != nil {
		slog.Info("transparent upstream", "src", src, "dst", dst, "err", err)
		return
	}
	defer upstream.Close()

	slog.Info("allow transparent", "src", src, "id", id, "host", host, "dst", dst)
	splicePeeked(pc, upstream)
}

// splicePeeked is splice() but the client side already has a *bufio.Reader
// holding the peeked bytes; we must use pc.Read (not the underlying Conn)
// to drain those first.
func splicePeeked(pc *peekedConn, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, pc); done <- struct{}{} }()
	go func() { _, _ = io.Copy(pc.Conn, upstream); done <- struct{}{} }()
	<-done
}

package proxy

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/crackbox/pkg/admin"
)

// stubOrigDst replaces origDst for the duration of a test, returning a
// fixed host:port. The real implementation reads SO_ORIGINAL_DST on a
// kernel socket — only iptables-REDIRECT'd connections have that set,
// so unit tests must inject.
func stubOrigDst(t *testing.T, ret string) {
	t.Helper()
	prev := origDst
	origDst = func(*net.TCPConn) (string, error) { return ret, nil }
	t.Cleanup(func() { origDst = prev })
}

func startTransparent(t *testing.T, reg *admin.Registry) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := New(reg)
	go func() {
		err := p.ServeTransparent(l)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Logf("ServeTransparent: %v", err)
		}
	}()
	return l
}

// TestTransparentUnsupportedPort: orig dst port outside {80,443} closes
// the connection before any peek or dial.
func TestTransparentUnsupportedPort(t *testing.T) {
	stubOrigDst(t, "10.0.0.1:9999")

	reg := admin.NewRegistry()
	l := startTransparent(t, reg)
	defer l.Close()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Proxy should close immediately. Read returns EOF / closed.
	c.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if n != 0 || !isCloseLike(err) {
		t.Errorf("expected close on unsupported port, got n=%d err=%v", n, err)
	}
}

// TestTransparentDeny: orig dst port = 443, valid TLS ClientHello, but
// the source IP is not registered → connection closed without upstream.
func TestTransparentDeny(t *testing.T) {
	stubOrigDst(t, "10.0.0.1:443")

	reg := admin.NewRegistry()
	l := startTransparent(t, reg)
	defer l.Close()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	raw := realClientHello(t, "example.com")
	if _, err := c.Write(raw); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if n != 0 || !isCloseLike(err) {
		t.Errorf("expected close on deny, got n=%d err=%v", n, err)
	}
}

// TestTransparentDenyOrigDstError: origDst returns an error (e.g.
// SO_ORIGINAL_DST not set) → connection closed without panic.
func TestTransparentDenyOrigDstError(t *testing.T) {
	prev := origDst
	origDst = func(*net.TCPConn) (string, error) { return "", errors.New("no orig dst") }
	t.Cleanup(func() { origDst = prev })

	reg := admin.NewRegistry()
	l := startTransparent(t, reg)
	defer l.Close()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if n != 0 || !isCloseLike(err) {
		t.Errorf("expected close on origDst error, got n=%d err=%v", n, err)
	}
}

// TestServeTransparentReturnsOnClose: Closing the listener unblocks the
// accept loop without error.
func TestServeTransparentReturnsOnClose(t *testing.T) {
	reg := admin.NewRegistry()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := New(reg)
	var wg sync.WaitGroup
	wg.Add(1)
	var serveErr error
	go func() {
		defer wg.Done()
		serveErr = p.ServeTransparent(l)
	}()
	time.Sleep(20 * time.Millisecond)
	l.Close()
	wg.Wait()
	if serveErr != nil {
		t.Errorf("ServeTransparent on close = %v want nil", serveErr)
	}
}

func isCloseLike(err error) bool {
	if err == nil || err == io.EOF {
		return err == io.EOF
	}
	s := err.Error()
	return strings.Contains(s, "use of closed") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "EOF")
}

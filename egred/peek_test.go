package main

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

// realClientHello dials a tls.Client to a pipe and captures the actual bytes
// it writes during handshake start. The server side is intentionally never
// completed — we only need the ClientHello on the wire.
func realClientHello(t *testing.T, host string) []byte {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	cfg := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	}
	tc := tls.Client(clientConn, cfg)
	go func() { _ = tc.Handshake() }()

	buf := make([]byte, 4096)
	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := serverConn.Read(buf)
	clientConn.Close()
	serverConn.Close()
	if n == 0 {
		t.Fatal("got zero bytes from tls.Client")
	}
	return buf[:n]
}

func TestParseSNIRealClientHello(t *testing.T) {
	hosts := []string{
		"api.anthropic.com",
		"github.com",
		"a.b.c.d.example.org",
	}
	for _, h := range hosts {
		raw := realClientHello(t, h)
		// strip the 5-byte TLS record header to get the handshake body
		if len(raw) < 6 || raw[0] != 0x16 {
			t.Fatalf("%s: bad record: %x", h, raw[:min(8, len(raw))])
		}
		got, err := parseSNI(raw[5:])
		if err != nil {
			t.Errorf("%s: parseSNI: %v", h, err)
			continue
		}
		if got != h {
			t.Errorf("parseSNI: got %q want %q", got, h)
		}
	}
}

func TestPeekTLSHostnameViaPeekedConn(t *testing.T) {
	raw := realClientHello(t, "example.com")
	c1, c2 := net.Pipe()
	go func() {
		c1.Write(raw)
		// Hold open so peek can complete. Real proxy will splice after peek.
		time.Sleep(200 * time.Millisecond)
		c1.Close()
	}()
	pc := newPeekedConn(c2)
	host, err := peekTLSHostname(pc, 1*time.Second)
	if err != nil {
		t.Fatalf("peekTLSHostname: %v", err)
	}
	if host != "example.com" {
		t.Errorf("got %q want %q", host, "example.com")
	}

	// After peek, the bytes must still be readable.
	buf := make([]byte, len(raw))
	c2.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, _ := pc.Read(buf)
	if n == 0 {
		t.Errorf("post-peek read returned 0 bytes")
	}
}

func TestParseSNIInvalid(t *testing.T) {
	tests := []struct{ name string; in []byte }{
		{"empty", nil},
		{"truncated", []byte{0x01, 0x00}},
		{"random garbage", []byte("hello world this is not tls")},
	}
	for _, tc := range tests {
		if _, err := parseSNI(tc.in); err == nil {
			t.Errorf("%s: expected error, got none", tc.name)
		}
	}
}

func TestPeekHTTPHost(t *testing.T) {
	tests := []struct {
		name string
		req  string
		want string
		err  bool
	}{
		{"basic", "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com", false},
		{"port", "GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n", "example.com:8080", false},
		{"case insensitive", "GET / HTTP/1.1\r\nhOsT: foo.com\r\n\r\n", "foo.com", false},
		{"with other headers", "POST /x HTTP/1.1\r\nUser-Agent: t\r\nHost: x.io\r\nContent-Length: 0\r\n\r\n", "x.io", false},
		{"missing host", "GET / HTTP/1.1\r\nUser-Agent: t\r\n\r\n", "", true},
	}
	for _, tc := range tests {
		c1, c2 := net.Pipe()
		go func(s string) { c1.Write([]byte(s)); time.Sleep(50 * time.Millisecond); c1.Close() }(tc.req)

		pc := newPeekedConn(c2)
		got, err := peekHTTPHost(pc, 4096, 1*time.Second)
		if tc.err {
			if err == nil {
				t.Errorf("%s: expected error, got %q", tc.name, got)
			}
		} else {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
			if !strings.EqualFold(got, tc.want) {
				t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
			}
		}
		c2.Close()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

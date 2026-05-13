package dns

import (
	"bytes"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// stubResolver implements Resolver with a fixed allowlist.
type stubResolver struct {
	allow map[string]bool
}

func (s *stubResolver) Allow(_ string, host string) (string, bool) {
	return "test", s.allow[host]
}

// stubForwarder records calls and returns a canned reply.
type stubForwarder struct {
	mu       sync.Mutex
	queries  [][]byte
	reply    []byte
	replyErr error
}

func (s *stubForwarder) forward(query []byte, _ *net.UDPAddr, _ time.Duration) ([]byte, error) {
	s.mu.Lock()
	cp := make([]byte, len(query))
	copy(cp, query)
	s.queries = append(s.queries, cp)
	s.mu.Unlock()
	if s.replyErr != nil {
		return nil, s.replyErr
	}
	if s.reply != nil {
		return s.reply, nil
	}
	// Default: echo back with QR=1, RCODE=0, and one tiny answer.
	r := make([]byte, len(query))
	copy(r, query)
	if len(r) >= 4 {
		r[2] = 0x81 // QR=1, RD echo
		r[3] = 0x80 // RA=1, RCODE=0
	}
	return r, nil
}

func startServer(t *testing.T, allow map[string]bool, fwd forwarder) (*Server, *net.UDPConn) {
	t.Helper()
	upstream, err := net.ResolveUDPAddr("udp", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("resolve upstream: %v", err)
	}
	s := &Server{
		allow:    &stubResolver{allow: allow},
		upstream: upstream,
		fwd:      fwd,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve("127.0.0.1:0")
	}()
	// Wait briefly for the listener to bind.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.LocalAddr() != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if s.LocalAddr() == nil {
		t.Fatalf("server did not bind")
	}
	t.Cleanup(func() { _ = s.Close(); <-done })

	// Client conn pointed at the server.
	cc, err := net.DialUDP("udp", nil, s.LocalAddr())
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return s, cc
}

// buildQuery encodes a minimal RFC 1035 query for (name, qtype, qclass=IN).
func buildQuery(id uint16, name string, qtype uint16) []byte {
	hdr := []byte{
		byte(id >> 8), byte(id),
		0x01, 0x00, // RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	q := append([]byte{}, hdr...)
	if name != "" && name != "." {
		labels := splitLabels(name)
		for _, l := range labels {
			q = append(q, byte(len(l)))
			q = append(q, []byte(l)...)
		}
	}
	q = append(q, 0x00)
	q = append(q, byte(qtype>>8), byte(qtype))
	q = append(q, 0x00, 0x01) // qclass=IN
	return q
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			if i > start {
				out = append(out, name[start:i])
			}
			start = i + 1
		}
	}
	if start < len(name) {
		out = append(out, name[start:])
	}
	return out
}

func readReply(t *testing.T, cc *net.UDPConn, timeout time.Duration) ([]byte, error) {
	t.Helper()
	if err := cc.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, _, err := cc.ReadFromUDP(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func TestDNS_AllowedHost_Forwards(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	q := buildQuery(0x1234, "example.com", 1) // A
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readReply(t, cc, 1*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(fwd.queries) != 1 {
		t.Fatalf("forwarder calls = %d, want 1", len(fwd.queries))
	}
	if !bytes.Equal(fwd.queries[0], q) {
		t.Errorf("forwarder got modified bytes")
	}
	if resp[2]&0x80 == 0 {
		t.Errorf("reply QR not set; flags=%02x %02x", resp[2], resp[3])
	}
}

func TestDNS_DeniedHost_NXDOMAIN(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	q := buildQuery(0xABCD, "blocked.test", 1)
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readReply(t, cc, 1*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(fwd.queries) != 0 {
		t.Fatalf("forwarder unexpectedly called %d times", len(fwd.queries))
	}
	// ID echoed.
	if resp[0] != 0xAB || resp[1] != 0xCD {
		t.Errorf("id not echoed: %02x %02x", resp[0], resp[1])
	}
	// RCODE=3 (NXDOMAIN).
	if rcode := resp[3] & 0x0f; rcode != rcodeNXDOMAIN {
		t.Errorf("rcode = %d, want %d", rcode, rcodeNXDOMAIN)
	}
	// QR=1.
	if resp[2]&0x80 == 0 {
		t.Errorf("QR not set")
	}
	// ANCOUNT/NSCOUNT/ARCOUNT all zero.
	if resp[6]|resp[7]|resp[8]|resp[9]|resp[10]|resp[11] != 0 {
		t.Errorf("non-zero AN/NS/AR counts")
	}
}

func TestDNS_ANYRecord_Refused(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	// Even an allowlisted host gets REFUSED for ANY.
	q := buildQuery(0x0001, "example.com", qtypeANY)
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readReply(t, cc, 1*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(fwd.queries) != 0 {
		t.Fatalf("forwarder called for ANY: %d", len(fwd.queries))
	}
	if rcode := resp[3] & 0x0f; rcode != rcodeRefused {
		t.Errorf("rcode = %d, want %d (REFUSED)", rcode, rcodeRefused)
	}
}

func TestDNS_AAAA_RespectsAllowlist(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	// AAAA for allowed → forwarded.
	q := buildQuery(0x1111, "example.com", 28) // AAAA
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readReply(t, cc, 1*time.Second); err != nil {
		t.Fatalf("read allowed AAAA: %v", err)
	}
	if len(fwd.queries) != 1 {
		t.Fatalf("forwarder calls = %d", len(fwd.queries))
	}

	// AAAA for denied → NXDOMAIN, no forward.
	q2 := buildQuery(0x2222, "blocked.test", 28)
	if _, err := cc.Write(q2); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readReply(t, cc, 1*time.Second)
	if err != nil {
		t.Fatalf("read denied AAAA: %v", err)
	}
	if rcode := resp[3] & 0x0f; rcode != rcodeNXDOMAIN {
		t.Errorf("denied AAAA rcode = %d, want NXDOMAIN", rcode)
	}
	if len(fwd.queries) != 1 {
		t.Errorf("forwarder called for denied AAAA")
	}
}

func TestDNS_MultipleQuestions_Dropped(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	q := buildQuery(0x3333, "example.com", 1)
	// Bump QDCOUNT to 2.
	q[4], q[5] = 0x00, 0x02
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := cc.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	buf := make([]byte, 1500)
	_, _, err := cc.ReadFromUDP(buf)
	if err == nil {
		t.Errorf("got reply for multi-question; should be dropped")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("expected timeout, got %v", err)
	}
	if len(fwd.queries) != 0 {
		t.Errorf("forwarder called for malformed")
	}
}

func TestDNS_CompressedQNAME_Dropped(t *testing.T) {
	fwd := &stubForwarder{}
	_, cc := startServer(t, map[string]bool{"example.com": true}, fwd)

	// Build a query with a single 0xC0-prefixed label (compression).
	q := []byte{
		0x44, 0x44, 0x01, 0x00,
		0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xC0, 0x0C, // pointer
		0x00, 0x01, 0x00, 0x01,
	}
	if _, err := cc.Write(q); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = cc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1500)
	if _, _, err := cc.ReadFromUDP(buf); err == nil {
		t.Errorf("got reply for compressed QNAME; should be dropped")
	}
	if len(fwd.queries) != 0 {
		t.Errorf("forwarder called for compressed")
	}
}

func TestDNS_ForwardSourceValidation(t *testing.T) {
	// End-to-end forwarder check: stand up a real upstream and a real
	// spoofer. The forwarder should ignore the spoofer's response.
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upstream.Close()

	// Real upstream: reply with QR=1, RCODE=0.
	go func() {
		buf := make([]byte, 1500)
		for {
			n, raddr, err := upstream.ReadFromUDP(buf)
			if err != nil {
				return
			}
			resp := make([]byte, n)
			copy(resp, buf[:n])
			resp[2] = 0x81
			resp[3] = 0x80
			_, _ = upstream.WriteToUDP(resp, raddr)
		}
	}()

	f := udpForwarder{}
	q := buildQuery(0x5555, "example.com", 1)
	upAddr, _ := upstream.LocalAddr().(*net.UDPAddr)
	resp, err := f.forward(q, upAddr, 1*time.Second)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp[0] != 0x55 || resp[1] != 0x55 {
		t.Errorf("upstream id mismatch")
	}
	if resp[2]&0x80 == 0 {
		t.Errorf("upstream did not set QR")
	}
}

func TestDNS_ParseQuestion_Roundtrip(t *testing.T) {
	cases := []struct {
		name  string
		qname string
		qtype uint16
	}{
		{"simple", "example.com", 1},
		{"deep", "a.b.c.example.com", 28},
		{"long-label", string(bytes.Repeat([]byte("x"), 63)) + ".example.com", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := buildQuery(0xFFFF, tc.qname, tc.qtype)
			got, qt, ok := parseQuestion(q)
			if !ok {
				t.Fatalf("parse failed")
			}
			if got != tc.qname {
				t.Errorf("name = %q, want %q", got, tc.qname)
			}
			if qt != tc.qtype {
				t.Errorf("qtype = %d, want %d", qt, tc.qtype)
			}
		})
	}
}

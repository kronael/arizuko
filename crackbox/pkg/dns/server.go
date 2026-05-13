// Package dns serves UDP/53 in front of crackbox's forward proxy.
// Per-query: parse first question, refuse ANY, look up the per-source-IP
// allowlist via admin.Registry, then either forward to the configured
// upstream resolver or synthesize NXDOMAIN. Stateless: no cache, no
// recursion, no zone data.
//
// Wire format follows RFC 1035; we parse only enough to extract the first
// question's QNAME and QTYPE. Pointer compression in QNAMEs is rejected
// as malformed (real resolvers don't compress queries).
package dns

import (
	"errors"
	"log/slog"
	"net"
	"time"
)

const (
	// rcodeNoError = 0
	rcodeFormErr = 1
	rcodeNXDOMAIN = 3
	rcodeRefused = 5

	qtypeANY = 255

	maxLabel = 63
	maxName  = 255
	maxIter  = 128
)

// Resolver is the registry subset the DNS server needs. Concrete type is
// *admin.Registry; interface lets tests inject a stub without spinning up
// a registry.
type Resolver interface {
	Allow(ip, host string) (id string, ok bool)
}

// Server holds configuration + the bound socket once Serve is running.
// One server per UDP listener. Close() ends Serve().
type Server struct {
	allow    Resolver
	upstream *net.UDPAddr
	fwd      forwarder // mockable in tests; defaults to udpForwarder{}

	conn *net.UDPConn // set by Serve; nil before
}

// New returns a Server configured to dispatch against allow and forward
// allowed queries to upstream. upstream must be host:port.
func New(allow Resolver, upstream string) (*Server, error) {
	if allow == nil {
		return nil, errors.New("dns: nil resolver")
	}
	addr, err := net.ResolveUDPAddr("udp", upstream)
	if err != nil {
		return nil, err
	}
	return &Server{allow: allow, upstream: addr, fwd: udpForwarder{}}, nil
}

// Serve listens on listen (e.g. ":53") until Close is called. Returns
// nil on graceful shutdown, an error on bind / fatal read failure.
func (s *Server) Serve(listen string) error {
	lAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	c, err := net.ListenUDP("udp", lAddr)
	if err != nil {
		return err
	}
	s.conn = c
	defer c.Close()

	buf := make([]byte, 1500) // standard MTU; DNS over UDP capped at 512 by default, 1232 with EDNS
	for {
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Copy: the handler may outlive buf if we ever go async; current
		// path is synchronous but the copy is cheap.
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		s.handle(c, src, pkt)
	}
}

// Close ends Serve. Safe to call once; subsequent calls are no-ops.
func (s *Server) Close() error {
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// LocalAddr returns the bound address. Useful for tests using :0.
func (s *Server) LocalAddr() *net.UDPAddr {
	if s.conn == nil {
		return nil
	}
	a, _ := s.conn.LocalAddr().(*net.UDPAddr)
	return a
}

func (s *Server) handle(c *net.UDPConn, src *net.UDPAddr, pkt []byte) {
	name, qtype, ok := parseQuestion(pkt)
	if !ok {
		// Malformed, multi-question, or compressed QNAME. Drop silently.
		return
	}
	if qtype == qtypeANY {
		resp := synthResponse(pkt, rcodeRefused)
		if len(resp) > 0 {
			_, _ = c.WriteToUDP(resp, src)
		}
		slog.Info("dns refuse any", "src", src.IP.String(), "name", name)
		return
	}
	id, allowed := s.allow.Allow(src.IP.String(), name)
	if !allowed {
		resp := synthResponse(pkt, rcodeNXDOMAIN)
		if len(resp) > 0 {
			_, _ = c.WriteToUDP(resp, src)
		}
		slog.Info("dns deny", "src", src.IP.String(), "id", id, "name", name)
		return
	}
	reply, err := s.fwd.forward(pkt, s.upstream, 3*time.Second)
	if err != nil {
		slog.Info("dns upstream", "src", src.IP.String(), "name", name, "err", err)
		return
	}
	_, _ = c.WriteToUDP(reply, src)
	slog.Info("dns allow", "src", src.IP.String(), "id", id, "name", name)
}

// parseQuestion returns the first question's name + qtype. Returns
// ok=false on any deviation from a clean single-question packet:
// short header, QDCOUNT != 1, label > 63, total name > 255, pointer
// compression in the question, or truncation.
func parseQuestion(pkt []byte) (name string, qtype uint16, ok bool) {
	if len(pkt) < 12 {
		return "", 0, false
	}
	qdcount := uint16(pkt[4])<<8 | uint16(pkt[5])
	if qdcount != 1 {
		return "", 0, false
	}
	off := 12
	var nameLen int
	var labels []string
	for i := 0; i < maxIter; i++ {
		if off >= len(pkt) {
			return "", 0, false
		}
		l := int(pkt[off])
		if l == 0 {
			off++
			if off+4 > len(pkt) {
				return "", 0, false
			}
			qtype = uint16(pkt[off])<<8 | uint16(pkt[off+1])
			// qclass = pkt[off+2:off+4]; ignored
			if len(labels) == 0 {
				return ".", qtype, true
			}
			return joinLabels(labels), qtype, true
		}
		if l&0xc0 != 0 {
			// Pointer compression in a query — reject.
			return "", 0, false
		}
		if l > maxLabel {
			return "", 0, false
		}
		if off+1+l > len(pkt) {
			return "", 0, false
		}
		nameLen += l + 1
		if nameLen > maxName {
			return "", 0, false
		}
		labels = append(labels, string(pkt[off+1:off+1+l]))
		off += 1 + l
	}
	return "", 0, false
}

func joinLabels(labels []string) string {
	// strings.Join allocates; manual concat is marginally cheaper but
	// not worth the code. Use a single allocation.
	n := 0
	for _, l := range labels {
		n += len(l) + 1
	}
	if n == 0 {
		return ""
	}
	b := make([]byte, 0, n-1)
	for i, l := range labels {
		if i > 0 {
			b = append(b, '.')
		}
		b = append(b, l...)
	}
	return string(b)
}

// synthResponse builds a response for query with given RCODE, echoing the
// original ID + question section. Empty AN/NS/AR. Preserves RD echo,
// sets QR=1, RA=1. Returns nil on malformed input (no question to echo).
func synthResponse(query []byte, rcode byte) []byte {
	if len(query) < 12 {
		return nil
	}
	// Find end of question section (one question only — we validated
	// QDCOUNT=1 in parseQuestion, but be defensive on malformed paths
	// like the ANY case where we trust the parser).
	off := 12
	for i := 0; i < maxIter; i++ {
		if off >= len(query) {
			return nil
		}
		l := query[off]
		if l == 0 {
			off++
			break
		}
		if l&0xc0 != 0 {
			return nil
		}
		if int(l) > maxLabel {
			return nil
		}
		if off+1+int(l) > len(query) {
			return nil
		}
		off += 1 + int(l)
	}
	if off+4 > len(query) {
		return nil
	}
	end := off + 4 // qtype(2) + qclass(2)

	resp := make([]byte, end)
	copy(resp, query[:end])
	// Flags: QR=1, OPCODE preserved (0), AA=0, TC=0, RD preserved, RA=1,
	// Z=0, RCODE=<arg>.
	origFlags := uint16(query[2])<<8 | uint16(query[3])
	rd := (origFlags >> 8) & 0x01
	newFlags := uint16(0x8000) | (rd << 8) | 0x0080 | uint16(rcode&0x0f)
	resp[2] = byte(newFlags >> 8)
	resp[3] = byte(newFlags)
	// QDCOUNT=1 (already), ANCOUNT/NSCOUNT/ARCOUNT=0.
	resp[6], resp[7] = 0, 0
	resp[8], resp[9] = 0, 0
	resp[10], resp[11] = 0, 0
	return resp
}

// rcodeFormErr is reserved for future use (currently unused; we drop
// malformed packets silently per spec).
var _ = rcodeFormErr

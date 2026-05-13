package dns

import (
	"errors"
	"net"
	"time"
)

// forwarder is the interface server uses to relay allowed queries to the
// upstream resolver. Production uses udpForwarder; tests inject a fake to
// assert call shape and return canned replies.
type forwarder interface {
	forward(query []byte, upstream *net.UDPAddr, timeout time.Duration) ([]byte, error)
}

type udpForwarder struct{}

// forward dials a fresh UDP socket to upstream, sends query, and reads
// one reply with a deadline. Reply is validated:
//   - source IP+port matches upstream;
//   - header ID matches query;
//   - echoed question section byte-equals the query's question section.
//
// A reply that fails any check is discarded and we keep reading until
// the deadline elapses — this prevents a same-network attacker from
// racing a forged response into the ephemeral socket.
func (udpForwarder) forward(query []byte, upstream *net.UDPAddr, timeout time.Duration) ([]byte, error) {
	if len(query) < 12 {
		return nil, errors.New("query too short")
	}
	qEnd, ok := questionEnd(query)
	if !ok {
		return nil, errors.New("query: bad question")
	}
	c, err := net.DialUDP("udp", nil, upstream)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := c.Write(query); err != nil {
		return nil, err
	}
	// We dialed (connected UDP), so reads only deliver datagrams from
	// the dialed remote. Belt-and-suspenders the source check anyway in
	// case the platform delivers ICMP errors as reads.
	buf := make([]byte, 4096)
	for {
		n, raddr, err := c.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		if raddr == nil || !raddr.IP.Equal(upstream.IP) || raddr.Port != upstream.Port {
			continue
		}
		if n < 12 {
			continue
		}
		if buf[0] != query[0] || buf[1] != query[1] {
			continue
		}
		rEnd, ok := questionEnd(buf[:n])
		if !ok || rEnd != qEnd {
			continue
		}
		match := true
		for i := 12; i < qEnd; i++ {
			if buf[i] != query[i] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out, nil
	}
}

// questionEnd returns the offset just past the question section's
// qtype+qclass. Mirrors the walk in synthResponse but accepts compressed
// names in REPLIES (responses commonly compress).
func questionEnd(pkt []byte) (int, bool) {
	if len(pkt) < 12 {
		return 0, false
	}
	off := 12
	for i := 0; i < maxIter; i++ {
		if off >= len(pkt) {
			return 0, false
		}
		l := pkt[off]
		if l == 0 {
			off++
			break
		}
		if l&0xc0 != 0 {
			off += 2 // pointer; question section ends here in some replies
			break
		}
		if int(l) > maxLabel {
			return 0, false
		}
		if off+1+int(l) > len(pkt) {
			return 0, false
		}
		off += 1 + int(l)
	}
	if off+4 > len(pkt) {
		return 0, false
	}
	return off + 4, true
}

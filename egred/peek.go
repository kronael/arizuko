package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// peekedConn wraps a net.Conn so the bytes consumed during peek are replayed
// to the upstream when we splice. We use a *bufio.Reader to do the peek and
// then construct an io.MultiReader from the buffered Read closure.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func newPeekedConn(c net.Conn) *peekedConn {
	return &peekedConn{Conn: c, r: bufio.NewReader(c)}
}

func (p *peekedConn) Read(b []byte) (int, error) {
	return p.r.Read(b)
}

// peekTLSHostname reads the ClientHello via bufio.Peek and returns the SNI
// hostname. The connection bytes are NOT consumed — splice can read them
// fresh through peekedConn.Read. Bounded read with deadline.
func peekTLSHostname(c *peekedConn, deadline time.Duration) (string, error) {
	if err := c.SetReadDeadline(time.Now().Add(deadline)); err != nil {
		return "", err
	}
	defer c.SetReadDeadline(time.Time{})

	// TLS record header is 5 bytes: type(1) version(2) length(2)
	hdr, err := c.r.Peek(5)
	if err != nil {
		return "", fmt.Errorf("peek tls header: %w", err)
	}
	if hdr[0] != 0x16 {
		return "", errors.New("not a tls handshake (first byte != 0x16)")
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen <= 0 || recLen > 16384 {
		return "", fmt.Errorf("bad tls record length: %d", recLen)
	}

	full, err := c.r.Peek(5 + recLen)
	if err != nil {
		return "", fmt.Errorf("peek tls record: %w", err)
	}
	return parseSNI(full[5:])
}

// parseSNI extracts the SNI hostname from a TLS ClientHello body.
// ClientHello layout (RFC 5246 §7.4.1.2 + RFC 6066 §3):
//
//	HandshakeType(1) | length(3) | client_version(2) | random(32) |
//	session_id_len(1) | session_id(...) | cipher_suites_len(2) |
//	cipher_suites(...) | compression_methods_len(1) | compression_methods(...) |
//	extensions_len(2) | extensions(...)
//
// Each extension: type(2) | length(2) | data(...)
// SNI extension type = 0; data = server_name_list_len(2) | (name_type(1) | name_len(2) | name(...))*
func parseSNI(body []byte) (string, error) {
	r := newReader(body)
	if _, err := r.u8(); err != nil {
		return "", err
	} // handshake type
	if _, err := r.u24(); err != nil {
		return "", err
	} // body length
	if _, err := r.u16(); err != nil {
		return "", err
	} // client_version
	if err := r.skip(32); err != nil {
		return "", err
	} // random
	sidLen, err := r.u8()
	if err != nil {
		return "", err
	}
	if err := r.skip(int(sidLen)); err != nil {
		return "", err
	}
	csLen, err := r.u16()
	if err != nil {
		return "", err
	}
	if err := r.skip(int(csLen)); err != nil {
		return "", err
	}
	cmLen, err := r.u8()
	if err != nil {
		return "", err
	}
	if err := r.skip(int(cmLen)); err != nil {
		return "", err
	}
	extTotal, err := r.u16()
	if err != nil {
		return "", err
	}

	end := r.off + int(extTotal)
	for r.off < end {
		extType, err := r.u16()
		if err != nil {
			return "", err
		}
		extLen, err := r.u16()
		if err != nil {
			return "", err
		}
		if extType != 0 { // not SNI
			if err := r.skip(int(extLen)); err != nil {
				return "", err
			}
			continue
		}
		// SNI extension
		listLen, err := r.u16()
		if err != nil {
			return "", err
		}
		listEnd := r.off + int(listLen)
		for r.off < listEnd {
			nameType, err := r.u8()
			if err != nil {
				return "", err
			}
			nameLen, err := r.u16()
			if err != nil {
				return "", err
			}
			if nameType != 0 { // host_name = 0
				if err := r.skip(int(nameLen)); err != nil {
					return "", err
				}
				continue
			}
			name, err := r.bytes(int(nameLen))
			if err != nil {
				return "", err
			}
			return strings.ToLower(string(name)), nil
		}
	}
	return "", errors.New("sni extension not found")
}

type byteReader struct {
	b   []byte
	off int
}

func newReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) u8() (uint8, error) {
	if r.off+1 > len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}
func (r *byteReader) u16() (uint16, error) {
	if r.off+2 > len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v, nil
}
func (r *byteReader) u24() (uint32, error) {
	if r.off+3 > len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := uint32(r.b[r.off])<<16 | uint32(r.b[r.off+1])<<8 | uint32(r.b[r.off+2])
	r.off += 3
	return v, nil
}
func (r *byteReader) skip(n int) error {
	if r.off+n > len(r.b) {
		return io.ErrUnexpectedEOF
	}
	r.off += n
	return nil
}
func (r *byteReader) bytes(n int) ([]byte, error) {
	if r.off+n > len(r.b) {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b, nil
}

// peekHTTPHost reads the request line + Host header without consuming bytes
// from the wire (using bufio.Peek). Bounded peek window to avoid attacker
// stalling. Returns lowercased host.
func peekHTTPHost(c *peekedConn, maxBytes int, deadline time.Duration) (string, error) {
	if err := c.SetReadDeadline(time.Now().Add(deadline)); err != nil {
		return "", err
	}
	defer c.SetReadDeadline(time.Time{})

	// Peek progressively until we find the end of the headers or hit max.
	for n := 256; n <= maxBytes; n *= 2 {
		buf, err := c.r.Peek(n)
		if err != nil && len(buf) == 0 {
			return "", err
		}
		if h, ok := scanHTTPHost(buf); ok {
			return strings.ToLower(strings.TrimSpace(h)), nil
		}
		// Reached end of headers without finding Host
		if i := indexCRLFCRLF(buf); i >= 0 {
			return "", errors.New("no host header")
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("host header not found within peek window")
}

func scanHTTPHost(buf []byte) (string, bool) {
	// Skip request line
	i := indexCRLF(buf, 0)
	if i < 0 {
		return "", false
	}
	off := i + 2
	for off < len(buf) {
		end := indexCRLF(buf, off)
		if end < 0 {
			return "", false
		}
		line := buf[off:end]
		if len(line) == 0 {
			return "", false // end of headers
		}
		colon := -1
		for j, b := range line {
			if b == ':' {
				colon = j
				break
			}
		}
		if colon > 0 && strings.EqualFold(string(line[:colon]), "host") {
			return string(line[colon+1:]), true
		}
		off = end + 2
	}
	return "", false
}

func indexCRLF(b []byte, from int) int {
	for i := from; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func indexCRLFCRLF(b []byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
}

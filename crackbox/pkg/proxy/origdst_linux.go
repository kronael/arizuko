//go:build linux

package proxy

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

const (
	soOriginalDst = 80 // include/uapi/linux/netfilter_ipv4.h
	ip6tSoOrigDst = 80 // same value, IPV6_ORIGINAL_DST
	solIPv6       = 41
)

// originalDst returns the pre-NAT destination of an iptables-REDIRECT'd
// connection. The kernel preserves it in a sockopt set on the listening
// socket; we read it here and return host:port.
func originalDst(c *net.TCPConn) (string, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return "", err
	}
	var (
		host string
		port int
		ipv6 bool
	)
	la := c.LocalAddr().(*net.TCPAddr)
	if la != nil && la.IP.To4() == nil && la.IP.To16() != nil {
		ipv6 = true
	}
	var ctlErr error
	err = rc.Control(func(fd uintptr) {
		if ipv6 {
			var sa6 syscall.RawSockaddrInet6
			sz := uint32(syscall.SizeofSockaddrInet6)
			_, _, errno := syscall.Syscall6(
				syscall.SYS_GETSOCKOPT,
				fd, solIPv6, ip6tSoOrigDst,
				uintptr(unsafe.Pointer(&sa6)), uintptr(unsafe.Pointer(&sz)), 0)
			if errno != 0 {
				ctlErr = fmt.Errorf("getsockopt v6 SO_ORIGINAL_DST: %v", errno)
				return
			}
			host = net.IP(sa6.Addr[:]).String()
			port = int(binary.BigEndian.Uint16(
				(*(*[2]byte)(unsafe.Pointer(&sa6.Port)))[:],
			))
		} else {
			var sa4 syscall.RawSockaddrInet4
			sz := uint32(syscall.SizeofSockaddrInet4)
			_, _, errno := syscall.Syscall6(
				syscall.SYS_GETSOCKOPT,
				fd, syscall.IPPROTO_IP, soOriginalDst,
				uintptr(unsafe.Pointer(&sa4)), uintptr(unsafe.Pointer(&sz)), 0)
			if errno != 0 {
				ctlErr = fmt.Errorf("getsockopt v4 SO_ORIGINAL_DST: %v", errno)
				return
			}
			host = net.IP(sa4.Addr[:]).String()
			port = int(binary.BigEndian.Uint16(
				(*(*[2]byte)(unsafe.Pointer(&sa4.Port)))[:],
			))
		}
	})
	if err != nil {
		return "", err
	}
	if ctlErr != nil {
		return "", ctlErr
	}
	return net.JoinHostPort(host, fmt.Sprint(port)), nil
}

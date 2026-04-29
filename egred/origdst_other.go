//go:build !linux

package main

import (
	"errors"
	"net"
)

func originalDst(c *net.TCPConn) (string, error) {
	return "", errors.New("SO_ORIGINAL_DST only supported on linux")
}

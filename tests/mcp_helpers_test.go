package tests

import "net"

// connIO adapts a net.Conn to mcp-go transport.NewIO's (reader, writeCloser).
// Shared by the split MCP-socket round-trip tests.
type connIO struct{ net.Conn }

func (c connIO) Close() error { return c.Conn.Close() }

package ipc

// A response the server cannot encode must still reach the agent. The agent is a
// JSON-RPC client on a unix socket, so a request answered by nothing at all
// blocks until its read deadline — 10.01s measured — and the turn spends that
// time waiting for a frame that is never coming (BUGS L231).

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// The tool runs first and the encoding fails after, which is the shape that
// makes this expensive: the call has taken effect and its answer is unsendable.
// A channel is what encoding/json refuses outright, so it stands in for whatever
// a future result type puts in StructuredContent.
func TestServeMCP_AResponseThatWillNotEncodeReachesTheCaller(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	stop, err := ServeMCP(sock, RoutdFns{}, StoreFns{}, "world", false, 0, "",
		func(srv *server.MCPServer) {
			srv.AddTool(mcp.NewTool("unencodable"),
				func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{StructuredContent: make(chan int)}, nil
				})
		})
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req := `{"jsonrpc":"2.0","id":7,"method":"tools/call",` +
		`"params":{"name":"unencodable","arguments":{}}}` + "\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("nothing was written for a response that would not encode, "+
			"so the caller waits out its read deadline: %v", err)
	}

	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *int   `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("the frame is not JSON: %v (%s)", err, resp)
	}
	if parsed.Error == nil {
		t.Fatalf("want an error frame, got %s", resp)
	}
	// A frame the caller cannot match to its request is a frame it keeps waiting
	// past.
	if parsed.ID == nil || *parsed.ID != 7 {
		t.Errorf("id = %v, want 7", parsed.ID)
	}
	if parsed.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", parsed.JSONRPC)
	}
	if parsed.Error.Code != -32603 {
		t.Errorf("code = %d, want -32603 (internal error)", parsed.Error.Code)
	}
	// Pinning the wording keeps the case honest: an error frame the MCP library
	// produced on its own would pass every assertion above while writeJSON's own
	// path went untouched.
	if !strings.Contains(parsed.Error.Message, "could not be encoded") {
		t.Errorf("message = %q, want the encode failure writeJSON sends", parsed.Error.Message)
	}
}

// The replacement frame is what a failed encode falls back to, so it must encode
// under every id it can be handed — an id that will not marshal becomes null
// rather than another dropped response.
func TestRPCEncodeFailure_EncodesUnderAnyID(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   any
		want any
	}{
		{"a number off the wire", float64(7), float64(7)},
		{"a string off the wire", "abc", "abc"},
		{"a notification carrying none", nil, nil},
		{"a value that will not marshal", make(chan int), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parsed map[string]any
			frame := rpcEncodeFailure(tc.id)
			if err := json.Unmarshal(frame, &parsed); err != nil {
				t.Fatalf("the replacement frame is not JSON: %v (%s)", err, frame)
			}
			if parsed["id"] != tc.want {
				t.Errorf("id = %v, want %v", parsed["id"], tc.want)
			}
			if parsed["error"] == nil {
				t.Errorf("no error object in %s", frame)
			}
		})
	}
}

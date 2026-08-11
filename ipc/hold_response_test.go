package ipc

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestHoldResponse_PendingIDIsTheContract — a recorded hold is a RESULT the
// agent reports and moves on from; an unrecordable one (empty id, BUGS J3) is an
// ERROR. Returning the pending shape without an id told the agent to wait for
// `/approve ` on nothing, which no operator could ever resolve.
func TestHoldResponse_PendingIDIsTheContract(t *testing.T) {
	held := holdResponse(7, "ab12", "mcp:delete")
	if held["error"] != nil {
		t.Fatalf("a recorded hold is not a failure: %+v", held)
	}
	res, ok := held["result"].(map[string]any)
	if !ok || res["pending"] != true || res["id"] != "ab12" {
		t.Fatalf("a recorded hold must carry pending+id: %+v", held)
	}

	lost := holdResponse(7, "", "mcp:delete")
	if lost["result"] != nil {
		t.Fatalf("a hold with no id must not look like a pending result: %+v", lost)
	}
	e, ok := lost["error"].(map[string]any)
	if !ok {
		t.Fatalf("a hold with no id must be a JSON-RPC error: %+v", lost)
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "mcp:delete") || strings.Contains(msg, "/approve") {
		t.Fatalf("error must name the tool and offer no id to approve: %q", msg)
	}
}

// TestHoldEligible_OnlyACallThatCouldRun — a pending-action row is a
// human-visible approval request, and the hold gate deliberately runs before
// per-call authz. With `hold:mcp:*` that let a raw caller mint unlimited rows
// for arbitrary unknown names, and probe whether an otherwise forbidden tool
// carried a hold rule, although execution could never pass authorization
// (BUGS J2).
func TestHoldEligible_OnlyACallThatCouldRun(t *testing.T) {
	noop := func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, nil
	}
	srv := server.NewMCPServer("test", "1.0")
	srv.AddTool(mcp.NewTool("send"), noop)
	srv.AddTool(mcp.NewTool("purge"), noop)

	// The caller holds `send` and not `purge` — the tools/list view (5/33).
	caller := StoreFns{Visible: func(name string) bool { return name == "send" }}

	if !holdEligible(srv, caller, "send") {
		t.Error("a registered tool the caller holds must reach the hold gate")
	}
	if holdEligible(srv, caller, "purge") {
		t.Error("a tool the caller cannot hold must not create a pending-action row")
	}
	if holdEligible(srv, caller, "delete_everything") {
		t.Error("an unregistered name must not create a pending-action row")
	}

	// Nil Visible is the operator/local socket: no row-based check, so
	// registration alone decides, and an unknown name is still refused.
	operator := StoreFns{}
	if !holdEligible(srv, operator, "purge") {
		t.Error("nil Visible must not block a registered tool")
	}
	if holdEligible(srv, operator, "delete_everything") {
		t.Error("nil Visible must still refuse an unregistered name")
	}
}

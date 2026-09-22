package ipc

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestHoldResponse_RefusalIsTheContract — a call the gate did not run comes back
// as a FAILED tool call carrying the verdict in words, so the agent cannot read
// it as "done" and can repeat the reason to the user. An unrecordable hold (empty
// refusal, BUGS J3) is a JSON-RPC error instead: there is no row, so there is no
// id anyone could ever approve, and offering one told the agent to wait forever.
func TestHoldResponse_RefusalIsTheContract(t *testing.T) {
	refused := holdResponse(7, "NOT RUN (id ab12): a human rejected mcp:delete.", "mcp:delete")
	if refused["error"] != nil {
		t.Fatalf("an answered hold is not a transport failure: %+v", refused)
	}
	res, ok := refused["result"].(map[string]any)
	if !ok || res["isError"] != true {
		t.Fatalf("a call that did not run must be an error result: %+v", refused)
	}
	content, _ := res["content"].([]map[string]any)
	if len(content) != 1 || !strings.Contains(content[0]["text"].(string), "rejected") {
		t.Fatalf("the verdict must reach the agent verbatim: %+v", res)
	}

	lost := holdResponse(7, "", "mcp:delete")
	if lost["result"] != nil {
		t.Fatalf("a hold with no row must not look like an answered call: %+v", lost)
	}
	e, ok := lost["error"].(map[string]any)
	if !ok {
		t.Fatalf("a hold with no row must be a JSON-RPC error: %+v", lost)
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

	// The caller holds `send` and not `purge` — the tools/list view (5/33-paths-roles).
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

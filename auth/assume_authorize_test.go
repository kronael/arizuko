package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
)

func TestAuthorize_ToolDeniesWithoutRow(t *testing.T) {
	s := openMem(t)
	caller := auth.Caller{Principal: "user:alice"}

	if auth.Authorize(s, caller, "mcp:send", "w/a", nil) {
		t.Error("mcp:send must deny with no acl row")
	}
	// A row scoped elsewhere must not leak across the tree.
	addRow(t, s, "user:alice", "mcp:send", "other/**", "allow")
	if auth.Authorize(s, caller, "mcp:send", "w/a", nil) {
		t.Error("mcp:send must not match a row scoped to a different subtree")
	}
	// The grant, once scoped to the target, is what permits it.
	addRow(t, s, "user:alice", "mcp:send", "w/**", "allow")
	if !auth.Authorize(s, caller, "mcp:send", "w/a", nil) {
		t.Error("mcp:send must be allowed by a row covering the target")
	}
}

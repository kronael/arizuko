package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
)

// Any future caller of Authorize(sub, "assume", ...) must be introduced
// together with the rows that permit it, or it fails closed and freezes every
// turn.
func TestAuthorize_AssumeDeniesWithoutRow(t *testing.T) {
	s := openMem(t)
	caller := auth.Caller{Principal: "user:alice"}

	if auth.Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must deny with no acl row")
	}
	// A row scoped elsewhere must not leak across the tree.
	addRow(t, s, "user:alice", "assume", "other/**", "allow")
	if auth.Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must not match a row scoped to a different subtree")
	}
	// The grant, once scoped to the target, is what permits it.
	addRow(t, s, "user:alice", "assume", "w/**", "allow")
	if !auth.Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must be allowed by a row covering the target")
	}
}

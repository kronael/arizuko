package auth

import "testing"

// TestActionCovers_AssumeIsNotUnderAdmin pins where `assume` sits in the action
// lattice. `assume` is identity-borrowing ("this turn may act as sub X"), not
// resource power over a folder, so folder `admin` must NOT confer it — an admin
// of corp/eng may drive the group's agent without thereby being able to make it
// speak as any member. Only a global `*` covers it, and `assume` is a leaf: it
// confers no interact/mcp: power of its own.
//
// The lattice already yields this by construction (actionCovers has no `admin`
// → assume edge). This test exists so a later edit to actionCovers — e.g.
// widening the `admin` case — cannot silently make folder admins impersonators.
func TestActionCovers_AssumeIsNotUnderAdmin(t *testing.T) {
	for _, c := range []struct {
		row, requested string
		want           bool
		why            string
	}{
		{"*", "assume", true, "global star covers every action"},
		{"assume", "assume", true, "exact match"},
		{"admin", "assume", false, "folder admin is resource power, not impersonation"},
		{"mcp:*", "assume", false, "tool wildcard must not reach a non-mcp action"},
		{"interact", "assume", false, "interact is the weakest action"},
		{"assume", "interact", false, "assume is a leaf: confers no resource power"},
		{"assume", "mcp:reply", false, "assume is a leaf: confers no tool power"},
		{"assume", "admin", false, "assume is a leaf: confers no admin"},
	} {
		if got := actionCovers(c.row, c.requested); got != c.want {
			t.Errorf("actionCovers(%q, %q) = %v, want %v — %s",
				c.row, c.requested, got, c.want, c.why)
		}
	}
}

// Any future caller of Authorize(sub, "assume", ...) must be introduced
// together with the rows that permit it, or it fails closed and freezes every
// turn.
func TestAuthorize_AssumeDeniesWithoutRow(t *testing.T) {
	s := openMem(t)
	caller := Caller{Principal: "user:alice"}

	if Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must deny with no acl row")
	}
	// A row scoped elsewhere must not leak across the tree.
	addRow(t, s, "user:alice", "assume", "other/**", "allow")
	if Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must not match a row scoped to a different subtree")
	}
	// The grant, once scoped to the target, is what permits it.
	addRow(t, s, "user:alice", "assume", "w/**", "allow")
	if !Authorize(s, caller, "assume", "w/a", nil) {
		t.Error("assume must be allowed by a row covering the target")
	}
}

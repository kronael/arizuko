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

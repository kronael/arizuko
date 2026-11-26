package auth_test

import (
	"testing"

	"github.com/kronael/arizuko/auth"
)

// A folder in role:operator reaches every action through the lattice, and
// that reach confers no guest capability.
func TestHasCapability_OperatorStarGrantsNothing(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("folder:atlas", "role:operator", "test"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "folder:atlas"}, "egress", "atlas", nil) {
		t.Fatal("precondition: the lattice covers egress")
	}
	if auth.HasCapability(s, "atlas", "egress", nil) {
		t.Fatal("a lattice row granted a guest capability")
	}
}

// A named row confers the capability on the scope its glob covers, and a wide
// deny takes it away.
func TestHasCapability_NamedRowAndWideDeny(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas/eng", "egress", "atlas/**", "allow")
	if !auth.HasCapability(s, "atlas/eng", "egress", nil) {
		t.Fatal("the named row granted nothing")
	}
	if auth.HasCapability(s, "atlas/eng", "web:publish", nil) {
		t.Fatal("an egress row granted web:publish")
	}
	addRow(t, s, "folder:atlas/eng", "*", "atlas/eng", "deny")
	if auth.HasCapability(s, "atlas/eng", "egress", nil) {
		t.Fatal("a deny on every action left the capability")
	}
}

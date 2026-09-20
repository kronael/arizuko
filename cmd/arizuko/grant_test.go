package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// newMem is the routd.db the grant, grant-action and route verbs write in
// production: every one of them reaches it through mustOpenACL → store.OpenRoutd.
// acl and routes live there, and routd's chain seeds role:member and
// role:operator into acl where the frozen pre-split chain seeds neither.
func newMem(t *testing.T) *store.Store {
	t.Helper()
	return testutils.NewRoutdStore(t)
}

func TestGrantThenList(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, core.ACLRow{Principal: "github:1", Scope: "alice"}, &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if !strings.Contains(out.String(), "alice") {
		t.Errorf("grant output = %q", out.String())
	}

	out.Reset()
	if err := runGrants(s, "github:1", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	if !strings.Contains(out.String(), "github:1") || !strings.Contains(out.String(), "alice") {
		t.Errorf("grants output missing row: %q", out.String())
	}
}

func TestGrantOperatorMembership(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, core.ACLRow{Principal: "github:1", Scope: "**"}, &out); err != nil {
		t.Fatalf("operator grant: %v", err)
	}
	if !strings.Contains(out.String(), "role:operator") {
		t.Errorf("expected role:operator output, got %q", out.String())
	}

	ancs := s.Ancestors("github:1")
	found := slices.Contains(ancs, "role:operator")
	if !found {
		t.Fatalf("expected role:operator in ancestors, got %v", ancs)
	}
}

func TestUngrantRemoves(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	runGrant(s, core.ACLRow{Principal: "github:1", Scope: "alice"}, &out)
	out.Reset()

	if err := runUngrant(s, core.ACLRow{Principal: "github:1", Scope: "alice"}, &out); err != nil {
		t.Fatalf("runUngrant: %v", err)
	}
	if !strings.Contains(out.String(), "ungranted") {
		t.Errorf("ungrant output = %q", out.String())
	}

	if rows, err := s.ListACL("github:1"); err != nil || len(rows) != 0 {
		t.Fatalf("expected 0 rows after ungrant, got %d", len(rows))
	}
}

func TestUngrantNonexistent(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	// Should not error even when no row exists; SQL DELETE is a no-op.
	if err := runUngrant(s, core.ACLRow{Principal: "ghost", Scope: "nowhere"}, &out); err != nil {
		t.Fatalf("runUngrant should not error: %v", err)
	}
}

func TestGrantsFilterBySub(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer

	runGrant(s, core.ACLRow{Principal: "u1", Scope: "alpha"}, &sink)
	runGrant(s, core.ACLRow{Principal: "u1", Scope: "beta"}, &sink)
	runGrant(s, core.ACLRow{Principal: "u2", Scope: "gamma"}, &sink)

	var out bytes.Buffer
	if err := runGrants(s, "u1", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	s1 := out.String()
	if !strings.Contains(s1, "alpha") || !strings.Contains(s1, "beta") {
		t.Errorf("u1 output missing own rows: %q", s1)
	}
	if strings.Contains(s1, "gamma") {
		t.Errorf("u1 output leaked u2 row: %q", s1)
	}
}

func TestGrantsEmpty(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runGrants(s, "nobody", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	if !strings.Contains(out.String(), "no grants") {
		t.Errorf("expected 'no grants', got %q", out.String())
	}
}

func TestGrantRejectsEmpty(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, core.ACLRow{Principal: "", Scope: "alice"}, &out); err == nil {
		t.Error("expected error for empty sub")
	}
	if err := runGrant(s, core.ACLRow{Principal: "u1", Scope: ""}, &out); err == nil {
		t.Error("expected error for empty pattern")
	}
	if err := runUngrant(s, core.ACLRow{Principal: "", Scope: "alice"}, &out); err == nil {
		t.Error("expected error for empty sub in ungrant")
	}
}

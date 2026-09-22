package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/routd"
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

// The CLI is the third write surface for an acl row, and it was the one that
// never read the alias warning — add_acl and dashd's form both do. A grant
// written against a linked alias creates a row that can never match, and the
// denial afterwards reads as policy working (BUGS L152, N2).
//
// This drives the renderer through the CLI's own resolver seam rather than
// re-testing routd.NonCanonicalWarning, which has its own tests: what is new
// here is that the CLI ASKS at all, and that it fails open.
func TestGrantWarnsOnALinkedAlias(t *testing.T) {
	const alias, canon = "telegram:user/7", "github:alice"

	var got []string
	warn := func(m string) { got = append(got, m) }
	if w := routd.NonCanonicalWarning(fakeIdentity{id: canon, claimed: true}, alias); w != "" {
		warn(w)
	}
	if len(got) != 1 || !strings.Contains(got[0], canon) {
		t.Fatalf("an alias grant produced %v; the operator is never told the row "+
			"cannot match", got)
	}

	got = nil
	if w := routd.NonCanonicalWarning(fakeIdentity{id: canon, claimed: true}, canon); w != "" {
		warn(w)
	}
	if len(got) != 0 {
		t.Errorf("a canonical grant warned anyway: %v — a warning on correct rows "+
			"is worse than the silence it replaces", got)
	}

	// Fail-open: no resolver is not a refusal. An operator standing at the CLI
	// with authd's store unreachable still gets the grant written.
	got = nil
	if w := routd.NonCanonicalWarning(nil, alias); w != "" {
		warn(w)
	}
	if len(got) != 0 {
		t.Errorf("no resolver produced a warning: %v", got)
	}
}

// The CLI has to ASK. Driving the renderer proves it renders; only this proves
// runGrant reaches it, which is the half that was missing on this surface while
// add_acl and dashd's form both had it.
func TestGrantAsksAboutThePrincipal(t *testing.T) {
	var asked []string
	prev := grantPrincipalWarn
	grantPrincipalWarn = func(principal string, _ func(string)) { asked = append(asked, principal) }
	t.Cleanup(func() { grantPrincipalWarn = prev })

	s := newMem(t)
	var out bytes.Buffer
	row := core.ACLRow{Principal: "telegram:user/7", Scope: "demo/**", Action: "admin"}
	if err := runGrant(s, row, &out); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !slices.Contains(asked, "telegram:user/7") {
		t.Errorf("the grant path never asked about the principal (asked=%v); the row "+
			"is written and an alias would never match", asked)
	}
}

// fakeIdentity stands in for authd without a store.
type fakeIdentity struct {
	id      string
	claimed bool
}

func (f fakeIdentity) Resolve(string) (ipc.Identity, []string, bool, error) {
	return ipc.Identity{ID: f.id}, nil, f.claimed, nil
}

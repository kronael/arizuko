package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// dropEntity takes one entity away from the store under test, which is how a
// test makes the read fail on purpose.
//
// The store is a network service, so "cannot answer" is an ordinary condition:
// an endpoint that is down, a key whose policy no longer covers the table, an
// owner that has never booted. A missing entity is the one a test can make
// deterministic, and the driver answers it the same way it answers the others.
func dropEntity(t *testing.T, s *Store, entity string) {
	t.Helper()
	d, ok := s.Obj().(*dynstore.Store)
	if !ok {
		t.Fatalf("this fixture is not served: %T", s.Obj())
	}
	if err := d.DropEntity(context.Background(), entity); err != nil {
		t.Fatalf("drop %s: %v", entity, err)
	}
}

// AllGroups is a visibility listing, and it must answer nil rather than a
// truncated list when the store cannot be read.
func TestAllGroups_FailsClosedOnStoreError(t *testing.T) {
	s := openMem(t)
	if err := s.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatal(err)
	}
	if got := s.AllGroups(); len(got) != 1 {
		t.Fatalf("precondition: AllGroups = %d, want 1", len(got))
	}
	dropEntity(t, s, Groups)
	if got := s.AllGroups(); got != nil {
		t.Errorf("AllGroups on an unreadable store = %+v, want nil (fail closed)", got)
	}
}

// BUGS A4: every ACL list read must return an ERROR when it cannot read, not an
// empty slice. Empty and broken used to be the same value, so a management
// surface answered 200 with `[]` for a policy it could not read at all, and a
// caller had no way to tell "this principal holds nothing" from "the read
// failed" — an operator acting on the first when the second is true re-grants
// what is already denied.
func TestACLLists_FailClosedOnStoreError(t *testing.T) {
	s := openMem(t)
	rows := []core.ACLRow{
		{Principal: "google:alice", Action: "admin", Scope: "atlas", Effect: "allow"},
		{Principal: "*", Action: "mcp:send", Scope: "atlas/**", Effect: "allow"},
	}
	for _, r := range rows {
		if err := s.AddACLRow(r); err != nil {
			t.Fatal(err)
		}
	}
	readers := map[string]func() ([]core.ACLRow, error){
		"ACLRowsFor":      func() ([]core.ACLRow, error) { return s.ACLRowsFor([]string{"google:alice"}) },
		"ListACL":         func() ([]core.ACLRow, error) { return s.ListACL("") },
		"ListACLByScope":  func() ([]core.ACLRow, error) { return s.ListACLByScope("atlas") },
		"ACLWildcardRows": func() ([]core.ACLRow, error) { return s.ACLWildcardRows() },
	}
	for name, read := range readers {
		got, err := read()
		if err != nil {
			t.Fatalf("precondition: %s: %v", name, err)
		}
		if len(got) == 0 {
			t.Fatalf("precondition: %s empty", name)
		}
	}

	dropEntity(t, s, ACL)

	for name, read := range readers {
		got, err := read()
		if err == nil {
			t.Errorf("%s on an unreadable store returned no error — an unreadable policy must not read as an empty one", name)
		}
		if len(got) != 0 {
			t.Errorf("%s on an unreadable store = %+v, want no rows alongside the error", name, got)
		}
	}
}

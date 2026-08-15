package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// Authz/visibility list reads MUST fail closed on any DB trouble, and must never
// return a truncated allow-list.
//
// For the ACL readers, failing closed means returning an ERROR (BUGS A4): they
// used to return a bare empty slice, which IS fail-closed for the evaluator but
// indistinguishable from "no grants exist" for everyone else. AllGroups is a
// visibility listing with no such consumer and still returns nil.
//
// The rows.Err() guard itself cannot be unit-forced with the modernc test DB:
// modernc buffers result rows, so closing the handle mid-iteration does not
// surface rows.Err() (verified empirically). These tests instead exercise the
// adjacent, deterministic fail-closed seam — the query-failure branch — by
// dropping the backing table after seeding, which drives the same contract
// (return nil/empty, not a partial slice). The rows.Err() branch is the same
// `return nil` on the iteration side and is covered by reading the source.

func TestAllGroups_FailsClosedOnQueryError(t *testing.T) {
	s := openMem(t)
	if err := s.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatal(err)
	}
	if got := s.AllGroups(); len(got) != 1 {
		t.Fatalf("precondition: AllGroups = %d, want 1", len(got))
	}
	if _, err := s.db.Exec(`DROP TABLE groups`); err != nil {
		t.Fatal(err)
	}
	if got := s.AllGroups(); got != nil {
		t.Errorf("AllGroups on broken table = %+v, want nil (fail closed)", got)
	}
}

func TestACLLists_FailClosedOnQueryError(t *testing.T) {
	s := openMem(t)
	rows := []core.ACLRow{
		{Principal: "google:alice", Action: "admin", Scope: "atlas", Effect: "allow"},
		{Principal: "*", Action: "send", Scope: "atlas/**", Effect: "allow"},
	}
	for _, r := range rows {
		if err := s.AddACLRow(r); err != nil {
			t.Fatal(err)
		}
	}
	// Preconditions: each list surfaces rows before the table is dropped.
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

	if _, err := s.db.Exec(`DROP TABLE acl`); err != nil {
		t.Fatal(err)
	}

	// BUGS A4: every list read must now return an ERROR, not an empty slice.
	// Empty and broken used to be the same value, so a management surface
	// answered 200 with `[]` for a table it could not read at all, and a caller
	// had no way to tell "this principal holds nothing" from "the read failed".
	for name, read := range readers {
		got, err := read()
		if err == nil {
			t.Errorf("%s on a broken table returned no error — an unreadable policy must not read as an empty one", name)
		}
		if len(got) != 0 {
			t.Errorf("%s on broken table = %+v, want no rows alongside the error", name, got)
		}
	}
}

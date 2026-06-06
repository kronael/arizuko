package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// Authz/visibility list reads MUST fail closed: on any DB trouble they return
// nil/empty, never a truncated allow-list (a partial list silently narrows or
// — worse for membership expansion — could be mistaken for "no grants exist").
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
	if len(s.ACLRowsFor([]string{"google:alice"})) == 0 {
		t.Fatal("precondition: ACLRowsFor empty")
	}
	if len(s.ListACL("")) == 0 {
		t.Fatal("precondition: ListACL empty")
	}
	if len(s.ListACLByScope("atlas")) == 0 {
		t.Fatal("precondition: ListACLByScope empty")
	}
	if len(s.ACLWildcardRows()) == 0 {
		t.Fatal("precondition: ACLWildcardRows empty")
	}

	if _, err := s.db.Exec(`DROP TABLE acl`); err != nil {
		t.Fatal(err)
	}

	// Every list read must now return nil/empty, never a partial allow-list.
	if got := s.ACLRowsFor([]string{"google:alice"}); len(got) != 0 {
		t.Errorf("ACLRowsFor on broken table = %+v, want empty", got)
	}
	if got := s.ListACL(""); len(got) != 0 {
		t.Errorf("ListACL on broken table = %+v, want empty", got)
	}
	if got := s.ListACLByScope("atlas"); len(got) != 0 {
		t.Errorf("ListACLByScope on broken table = %+v, want empty", got)
	}
	if got := s.ACLWildcardRows(); len(got) != 0 {
		t.Errorf("ACLWildcardRows on broken table = %+v, want empty", got)
	}
	if got := s.UserScopes("google:alice"); len(got) != 0 {
		t.Errorf("UserScopes on broken table = %+v, want empty", got)
	}
}

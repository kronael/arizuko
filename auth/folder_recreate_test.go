package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestRecreatedFolder_InheritsNoGrant is BUGS L198 stated as an assertion. The
// old tenant's admin held `*` on `acme/**` and a folder-scoped credential; after
// the folder is deleted and a different tenant takes the same path, neither may
// still be there. The verdict comes from auth.Authorize, the one evaluator, so
// the test cannot pass by agreeing with a hand-written ACL query.
func TestRecreatedFolder_InheritsNoGrant(t *testing.T) {
	s := openMem(t)
	if err := s.PutGroup(core.Group{Folder: "acme", AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	if err := s.PutGroup(core.Group{Folder: "acme/eng", AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroup child: %v", err)
	}
	// A sibling whose name starts with the same characters: the purge must not
	// widen onto it, or a green result would only prove the table was emptied.
	if err := s.PutGroup(core.Group{Folder: "acmecorp", AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroup sibling: %v", err)
	}
	addRow(t, s, "google:oldadmin", "*", "acme/**", "allow")
	addRow(t, s, "google:oldadmin", "*", "acme/eng/**", "allow")
	addRow(t, s, "google:neighbour", "*", "acmecorp/**", "allow")
	s.SetSecretKeys([]byte("test-key"))
	if err := s.SetSecret(store.ScopeFolder, "acme", "GITHUB_TOKEN", "old-tenant-pat"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	old := auth.Caller{Principal: "google:oldadmin"}
	if !auth.Authorize(s, old, "interact", "acme/inbox", nil) {
		t.Fatal("seed: the old admin must hold acme before the delete")
	}

	removed, _, err := s.DeleteGroup("acme")
	if err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if removed != 2 {
		t.Fatalf("groups removed = %d, want 2 (acme + acme/eng)", removed)
	}

	// A different tenant takes the path.
	if err := s.PutGroup(core.Group{Folder: "acme", AddedAt: time.Now()}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if auth.Authorize(s, old, "interact", "acme/inbox", nil) {
		t.Fatal("the re-created folder inherited the old tenant's admin grant")
	}
	if auth.Authorize(s, old, "interact", "acme/eng/inbox", nil) {
		t.Fatal("the re-created folder inherited the old tenant's child-scoped grant")
	}
	if _, err := s.GetSecret(store.ScopeFolder, "acme", "GITHUB_TOKEN"); !errors.Is(err, store.ErrSecretNotFound) {
		t.Fatalf("the re-created folder inherited the old tenant's credential: err=%v", err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "google:neighbour"}, "interact", "acmecorp/inbox", nil) {
		t.Fatal("the purge widened onto a sibling folder whose name shares a prefix")
	}
}

// TestRecreatedFolder_InheritsNoMembership: the folder principal's own edges go
// too. A folder made an operator by acl_membership stays one across a delete if
// the edge outlives the row, because Caller.Route expands through
// FolderPrincipal into whatever parents it names.
func TestRecreatedFolder_InheritsNoMembership(t *testing.T) {
	s := openMem(t)
	if err := s.PutGroup(core.Group{Folder: "acme", AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	if err := s.PutMembership(auth.FolderPrincipal("acme"), "role:privileged", "test"); err != nil {
		t.Fatalf("PutMembership: %v", err)
	}
	addRow(t, s, "role:privileged", "*", "**", "allow")

	route := auth.Caller{Route: "acme"}
	if !auth.Authorize(s, route, "interact", "elsewhere", nil) {
		t.Fatal("seed: the folder must reach the privileged role before the delete")
	}

	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if err := s.PutGroup(core.Group{Folder: "acme", AddedAt: time.Now()}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if auth.Authorize(s, route, "interact", "elsewhere", nil) {
		t.Fatal("the re-created folder inherited the old tenant's role membership")
	}
}

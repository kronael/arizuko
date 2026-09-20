package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder is BUGS L247 stated as
// an assertion. POST /dash/groups/new writes the group row, the member edge,
// the creator's admin grant and the audit row in ONE transaction, and
// `specs/6/38` names that shape security-bearing: the escalation it prevents is
// a folder whose creator holds no admin on it, or holds one the audit does not
// record.
//
// It drives the HANDLER, not CreateGroupWithAdmin. A fixture that called the
// store method would prove the method and leave the page — the part that
// resolves the caller and decides the admin principal — untested, which is the
// hole the entry names.
func TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	t.Setenv("DATA_DIR", t.TempDir())

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/new", "folder=team2", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST create = %d body=%q, want 303", w.Code, w.Body.String())
	}

	if !hasFolder(t, routd, "team2") {
		t.Fatal("the group row is missing, so the folder was never created")
	}
	// The grant is read back as a ROW. auth.Authorize alone cannot see it here:
	// the creator already holds `**`, so the evaluator answers yes whether or
	// not this transaction wrote anything — a mutation that scoped the grant to
	// the wrong folder passed an Authorize-only assertion.
	rows, err := store.NewOver(routd).ListACL("alice@x")
	if err != nil {
		t.Fatalf("read back the creator's grants: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "admin" && row.Scope == "team2/**" && row.Effect == "allow" {
			found = true
		}
	}
	if !found {
		t.Errorf("no `admin` grant on team2/** for the creator; got %+v", rows)
	}
	// And the evaluator honours it, on the folder AND on a child of it. A bare
	// `team2` scope reads as admin of the folder and 403 on every child, because
	// matchSegments ends on len(in) == 0 (BUGS L206).
	creator := auth.Caller{Principal: "alice@x"}
	if !auth.Authorize(store.NewOver(routd), creator, "admin", "team2", nil) {
		t.Error("the creator holds no admin on the folder they just created")
	}
	if !auth.Authorize(store.NewOver(routd), creator, "admin", "team2/sre", nil) {
		t.Error("the creator holds no admin on a CHILD of the folder they just created")
	}
	if got := auditActor(t, d, "group.create"); got != "alice@x" {
		t.Errorf("group.create actor_sub = %q, want alice@x", got)
	}
}

// A folder the caller may not administer is refused BEFORE anything is written.
// requireAdmin runs after the folder syntax checks, so a partial write here
// would leave a group nobody was allowed to make.
func TestGroupCreatePage_RefusesAFolderTheCallerCannotAdminister(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	t.Setenv("DATA_DIR", t.TempDir())
	seedAdmin(t, routd, "bob@x", "bobland")

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/new", "folder=elsewhere", "bob@x"))
	if w.Code == http.StatusSeeOther {
		t.Fatalf("create succeeded for a folder bob may not administer")
	}
	if hasFolder(t, routd, "elsewhere") {
		t.Error("the refusal still wrote the group row")
	}
}

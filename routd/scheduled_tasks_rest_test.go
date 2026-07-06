package routd

// REST-face guard tests for the spec 5/44 scheduled_tasks fold: /v1/tasks
// list/get/patch/delete now ride the SAME shared scheduledTasksHandler the agent
// MCP tools use, via resreg.RegisterREST + the injected tasksRESTCaller/
// tasksRESTGate. tasks_test.go covers the CRUD parity with a service token
// (empty JWT folder = root); these cover the two containment guards a scoped
// operator must hit:
//   - PER-TASK containment (patch/delete a SIBLING folder's task → 403), the
//     handler's AuthorizeStructural cap. Fails if that cap is dropped.
//   - LIST-ALL no-leak: a folder-SCOPED tier-0 token lists ONLY its own tasks,
//     never a sibling's. Fails if list-all is keyed on tier-0 instead of the
//     empty JWT folder claim.

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/core"
)

// TestRESTTaskScopedSelfService: a scoped non-operator (tasks:write:own_group at
// a tier-2 folder) cancels/patches its OWN folder's task but is 403'd on a
// SIBLING folder's task by the handler's per-task AuthorizeStructural — the same
// cap pause/resume/cancel enforce over the agent socket. Fails (the sibling task
// would be deleted/paused) if that structural cap is dropped.
func TestRESTTaskScopedSelfService(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:write:own_group"}, folder: "world/a/b"})
	_ = db.PutGroup(core.Group{Folder: "world/a/b"})
	_ = db.PutGroup(core.Group{Folder: "world/a/c"})
	seedTask(t, db, "own", "world/a/b", "web:own", "mine")
	seedTask(t, db, "sibling", "world/a/c", "web:sib", "theirs")

	// own task DELETE → cancel: allowed, {ok:true}, row gone.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/own", "", nil); r.Code != 200 {
		t.Fatalf("own cancel = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("own"); ok {
		t.Fatal("own task not cancelled")
	}

	// sibling DELETE → cancel: 403 by the per-task cap, row untouched.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/sibling", "", nil); r.Code != 403 {
		t.Fatalf("sibling cancel = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("sibling"); !ok {
		t.Fatal("denied cross-folder cancel still deleted the sibling task")
	}

	// sibling PATCH → pause: also 403, status untouched (still active).
	if r := doJSON(t, h, "PATCH", "/v1/tasks/sibling", "",
		map[string]string{"status": core.TaskPaused}); r.Code != 403 {
		t.Fatalf("sibling patch = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if got, _ := db.GetTask("sibling"); got.Status != core.TaskActive {
		t.Fatalf("denied cross-folder patch changed sibling status to %q", got.Status)
	}
}

// TestRESTTaskScopedPatchOwn: a scoped operator patches its OWN folder's task
// (pause) end-to-end — proves the containment isn't a blanket deny.
func TestRESTTaskScopedPatchOwn(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:write:own_group"}, folder: "world/a/b"})
	_ = db.PutGroup(core.Group{Folder: "world/a/b"})
	seedTask(t, db, "own", "world/a/b", "web:own", "mine")

	if r := doJSON(t, h, "PATCH", "/v1/tasks/own", "",
		map[string]string{"status": core.TaskPaused}); r.Code != 200 {
		t.Fatalf("own patch = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if got, _ := db.GetTask("own"); got.Status != core.TaskPaused {
		t.Fatalf("own patch: status=%q want paused", got.Status)
	}
}

// TestRESTTaskScopedListNoLeak is the list-all leak guard: "alice" is a top-level
// folder (tier 0), but a folder-SCOPED token there must list ONLY its own tasks,
// never a sibling's. Keying list-all on tier-0 (instead of the empty JWT folder
// claim = root/service token) would leak every folder's tasks to any top-level
// tenant. Content-level, because a status-only check can't tell "own-only, 200"
// from "all, 200".
func TestRESTTaskScopedListNoLeak(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:read:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "bob"})
	seedTask(t, db, "a-1", "alice", "web:alice", "mine")
	seedTask(t, db, "b-1", "bob", "web:bob", "theirs")

	g := doGet(t, h, "/v1/tasks")
	if g.Code != 200 {
		t.Fatalf("alice list = %d want 200 body=%s", g.Code, g.Body.String())
	}
	var listed []core.Task
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil {
		t.Fatalf("alice list body %s: %v", g.Body.String(), err)
	}
	if len(listed) != 1 || listed[0].Owner != "alice" {
		t.Fatalf("alice (tier-0 scoped) list = %+v want ONLY alice's — bob's task leaked", listed)
	}
}

// TestRESTTaskRootListsAll: the root/service token (empty JWT folder) still sees
// every folder's tasks — the other side of the list-all guard.
func TestRESTTaskRootListsAll(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:timed", scope: []string{"tasks:read"}})
	_ = db.PutGroup(core.Group{Folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "bob"})
	seedTask(t, db, "a-1", "alice", "web:alice", "mine")
	seedTask(t, db, "b-1", "bob", "web:bob", "theirs")

	g := doGet(t, h, "/v1/tasks")
	if g.Code != 200 {
		t.Fatalf("root list = %d want 200 body=%s", g.Code, g.Body.String())
	}
	var listed []core.Task
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil {
		t.Fatalf("root list body %s: %v", g.Body.String(), err)
	}
	if len(listed) != 2 {
		t.Fatalf("root list = %d tasks want 2 (all folders) %+v", len(listed), listed)
	}
}

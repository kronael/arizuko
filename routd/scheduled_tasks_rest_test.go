package routd

// REST-face guard tests for the spec 5/16 scheduled_tasks fold: /v1/tasks
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

// TestRESTTaskTier0NoCrossTenant is the cross-tenant hole the 5/16 containment
// decouple closes: "alice" is a top-level tenant (tier 0), so the baked
// AuthorizeStructural tier cap is a no-op for it — tier 0 passes for ANY task
// owner. Before the decouple a DELETE of "bob"'s task returned 200 and deleted it.
// The REST face must contain on ownsFolder, not the tier: a sibling tenant's task
// is 403 and untouched, while the caller's own task still cancels.
func TestRESTTaskTier0NoCrossTenant(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:write:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "bob"})
	seedTask(t, db, "alice-1", "alice", "web:alice", "mine")
	seedTask(t, db, "bob-1", "bob", "web:bob", "theirs")

	// cross-tenant DELETE → 403, bob's task untouched (was 200 + deleted).
	if r := doJSON(t, h, "DELETE", "/v1/tasks/bob-1", "", nil); r.Code != 403 {
		t.Fatalf("cross-tenant cancel = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("bob-1"); !ok {
		t.Fatal("cross-tenant cancel deleted another tenant's task")
	}
	// own DELETE → 200, gone.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/alice-1", "", nil); r.Code != 200 {
		t.Fatalf("own cancel = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("alice-1"); ok {
		t.Fatal("own task not cancelled")
	}
}

// TestRESTTaskGetNoCrossTenant is the read twin of the cross-tenant leak the
// DELETE cap closed: GET /v1/tasks/{id} let any tasks:read holder read ANY task
// by ID, since the Gate's ownsFolder(jwt,jwt) is a no-op for a per-task op. Now
// contain() gates the read on the task's OWNER — a tenant reads only its subtree;
// a root/operator token (empty folder) still reads any.
func TestRESTTaskGetNoCrossTenant(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:read:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "bob"})
	seedTask(t, db, "alice-1", "alice", "web:alice", "mine")
	seedTask(t, db, "bob-1", "bob", "web:bob", "theirs")

	// cross-tenant GET → 403 (was 200 + leaked the task row).
	if r := doJSON(t, h, "GET", "/v1/tasks/bob-1", "", nil); r.Code != 403 {
		t.Fatalf("cross-tenant get = %d want 403 body=%s", r.Code, r.Body.String())
	}
	// own GET → 200.
	if r := doJSON(t, h, "GET", "/v1/tasks/alice-1", "", nil); r.Code != 200 {
		t.Fatalf("own get = %d want 200 body=%s", r.Code, r.Body.String())
	}
}

// TestRESTTaskTier1NoWorldLeak: "alice/team" (tier 1) shares world "alice" with
// "alice/other" but does NOT contain it. The baked cap authorized tier 1 for ANY
// task in its WORLD (isInWorld), so DELETE/PATCH of "alice/other"'s task returned
// 200 — a same-world over-permission. Post-decouple the REST face uses ownsFolder
// (own-or-descendant): a same-world sibling is 403 and untouched, while the
// caller's own and a descendant's task still work.
func TestRESTTaskTier1NoWorldLeak(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:write:own_group"}, folder: "alice/team"})
	for _, f := range []string{"alice", "alice/team", "alice/team/sub", "alice/other"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	seedTask(t, db, "own", "alice/team", "web:own", "mine")
	seedTask(t, db, "child", "alice/team/sub", "web:child", "descendant")
	seedTask(t, db, "world-sib", "alice/other", "web:sib", "same world, not mine")

	// same-world sibling DELETE → 403, untouched (was 200 + deleted).
	if r := doJSON(t, h, "DELETE", "/v1/tasks/world-sib", "", nil); r.Code != 403 {
		t.Fatalf("world-sibling cancel = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("world-sib"); !ok {
		t.Fatal("world-sibling cancel deleted a same-world task outside the subtree")
	}
	// same-world sibling PATCH → 403, status untouched.
	if r := doJSON(t, h, "PATCH", "/v1/tasks/world-sib", "",
		map[string]string{"status": core.TaskPaused}); r.Code != 403 {
		t.Fatalf("world-sibling patch = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if got, _ := db.GetTask("world-sib"); got.Status != core.TaskActive {
		t.Fatalf("world-sibling patch changed status to %q", got.Status)
	}
	// own DELETE → 200.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/own", "", nil); r.Code != 200 {
		t.Fatalf("own cancel = %d want 200 body=%s", r.Code, r.Body.String())
	}
	// descendant PATCH → 200.
	if r := doJSON(t, h, "PATCH", "/v1/tasks/child", "",
		map[string]string{"status": core.TaskPaused}); r.Code != 200 {
		t.Fatalf("descendant patch = %d want 200 body=%s", r.Code, r.Body.String())
	}
}

// TestRESTTaskTier2Descendant: a tier-2 folder "world/a/b" manages its OWN and its
// DESCENDANTS' tasks over REST. The baked cap denied tier 2 anything but an EXACT
// owner match (task.Owner != id.Folder → 403), so a descendant's task was wrongly
// 403. Post-decouple the REST face uses ownsFolder: own + descendant are 200 while
// a sibling is 403.
func TestRESTTaskTier2Descendant(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"tasks:write:own_group"}, folder: "world/a/b"})
	for _, f := range []string{"world/a/b", "world/a/b/child", "world/a/c"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	seedTask(t, db, "own", "world/a/b", "web:own", "mine")
	seedTask(t, db, "child", "world/a/b/child", "web:child", "descendant")
	seedTask(t, db, "sibling", "world/a/c", "web:sib", "not mine")

	// descendant cancel → 200 (was 403 under the exact-match cap).
	if r := doJSON(t, h, "DELETE", "/v1/tasks/child", "", nil); r.Code != 200 {
		t.Fatalf("descendant cancel = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("child"); ok {
		t.Fatal("descendant task not cancelled")
	}
	// sibling cancel → 403, untouched.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/sibling", "", nil); r.Code != 403 {
		t.Fatalf("sibling cancel = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.GetTask("sibling"); !ok {
		t.Fatal("sibling cancel deleted a task outside the subtree")
	}
	// own cancel → 200.
	if r := doJSON(t, h, "DELETE", "/v1/tasks/own", "", nil); r.Code != 200 {
		t.Fatalf("own cancel = %d want 200 body=%s", r.Code, r.Body.String())
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

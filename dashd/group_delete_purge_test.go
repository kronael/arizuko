package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestGroupDeletePage_RecreatedFolderInheritsNothing is BUGS L198 stated as an
// assertion on the OPERATOR path. Both deletes have to reach the same purge, so
// the same folder re-created after the delete page ran must hold none of the
// previous tenant's authority — the grant this page always removed, and the
// scheduled task and engagement it did not.
//
// The verdict on the grant comes from auth.Authorize, the one evaluator.
func TestGroupDeletePage_RecreatedFolderInheritsNothing(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedGrant(t, routd, core.ACLRow{
		Principal: "google:oldadmin", Action: "*", Scope: "team/**", Effect: "allow",
	})
	nightly := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	seedScheduledTask(t, routd, core.Task{
		ID: "team-nightly", Owner: "team", ChatJID: "web:team", Prompt: "/compact",
		Cron: "0 2 * * *", NextRun: &nightly, Status: "active",
	})
	if err := store.NewOver(routd).SetLastReply("web:team", "", "m1", "team"); err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	// A sibling sharing the leading characters: a green result must mean the
	// subtree went, not the table.
	seedFolder(t, routd, "teamwork")
	seedScheduledTask(t, routd, core.Task{
		ID: "teamwork-nightly", Owner: "teamwork", ChatJID: "web:teamwork",
		Prompt: "/compact", Cron: "0 2 * * *", NextRun: &nightly, Status: "active",
	})

	st := store.NewOver(routd)
	old := auth.Caller{Principal: "google:oldadmin"}
	if !auth.Authorize(st, old, "interact", "team/inbox", nil) {
		t.Fatal("seed: the old admin must hold team before the delete")
	}

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}

	// A different tenant takes the path.
	if err := store.NewOver(routd).PutGroupRow(core.Group{
		Folder: "team", AddedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if auth.Authorize(st, old, "interact", "team/inbox", nil) {
		t.Fatal("the re-created folder inherited the old tenant's admin grant")
	}
	if n := countTasks(t, routd, func(task core.Task) bool { return task.Owner == "team" }); n != 0 {
		t.Errorf("scheduled_tasks for the deleted folder survived: %d", n)
	}
	if id := store.NewOver(routd).LastReplyID("web:team", ""); id != "" {
		t.Errorf("the deleted folder's reply state survived: last_reply_id = %q", id)
	}
	if !hasFolder(t, routd, "teamwork") {
		t.Error("the purge widened onto the sibling folder, which is gone")
	}
	if n := countTasks(t, routd, func(task core.Task) bool { return task.Owner == "teamwork" }); n != 1 {
		t.Errorf("the purge widened onto the sibling folder's tasks: %d rows left", n)
	}
	if got := auditActor(t, d, "group.delete"); got != "alice@x" {
		t.Errorf("group.delete actor_sub = %q, want alice@x", got)
	}
}

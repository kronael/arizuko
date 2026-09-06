package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/auth"
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
	if _, err := routd.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at)
		 VALUES ('google:oldadmin', '*', 'team/**', 'allow', '')`); err != nil {
		t.Fatalf("seed old-tenant grant: %v", err)
	}
	if _, err := routd.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES ('team-nightly', 'team', 'web:team', '/compact', '0 2 * * *', '2026-01-01', 'active', '')`); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := routd.Exec(
		`INSERT INTO chat_reply_state (jid, topic, last_reply_id, engaged_folder)
		 VALUES ('web:team', '', 'm1', 'team')`); err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	// A sibling sharing the leading characters: a green result must mean the
	// subtree went, not the table.
	if _, err := routd.Exec(
		`INSERT INTO groups (folder, added_at) VALUES ('teamwork', '')`); err != nil {
		t.Fatalf("seed sibling: %v", err)
	}
	if _, err := routd.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES ('teamwork-nightly', 'teamwork', 'web:teamwork', '/compact', '0 2 * * *', '2026-01-01', 'active', '')`); err != nil {
		t.Fatalf("seed sibling task: %v", err)
	}

	st := store.New(routd)
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
	if _, err := routd.Exec(
		`INSERT INTO groups (folder, added_at) VALUES ('team', '')`); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if auth.Authorize(st, old, "interact", "team/inbox", nil) {
		t.Fatal("the re-created folder inherited the old tenant's admin grant")
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM scheduled_tasks WHERE owner = 'team'`); n != 0 {
		t.Errorf("scheduled_tasks for the deleted folder survived: %d", n)
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM chat_reply_state WHERE engaged_folder = 'team'`); n != 0 {
		t.Errorf("chat_reply_state for the deleted folder survived: %d", n)
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM groups WHERE folder = 'teamwork'`); n != 1 {
		t.Errorf("the purge widened onto the sibling folder: %d rows left", n)
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM scheduled_tasks WHERE owner = 'teamwork'`); n != 1 {
		t.Errorf("the purge widened onto the sibling folder's tasks: %d rows left", n)
	}
	if got := auditActor(t, d, "group.delete"); got != "alice@x" {
		t.Errorf("group.delete actor_sub = %q, want alice@x", got)
	}
}

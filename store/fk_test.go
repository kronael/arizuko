package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// FK tests use file-backed Open() (not OpenMem) because OpenMem does not
// set PRAGMA foreign_keys=ON. Production paths run with FK enforcement
// via store.Open; these tests mirror that.

func openFKStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestFK_WebRoutes_RejectsUnknownFolder verifies migration 0068:
// web_routes.folder → groups.folder rejects rows whose folder has
// no parent group.
func TestFK_WebRoutes_RejectsUnknownFolder(t *testing.T) {
	s := openFKStore(t)
	err := s.SetWebRoute(WebRoute{
		PathPrefix: "/pub/ghost/",
		Access:     "public",
		Folder:     "ghost-folder-with-no-group",
	})
	if err == nil {
		t.Fatal("expected FK violation, got nil")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") &&
		!strings.Contains(err.Error(), "constraint") {
		t.Fatalf("expected FK error, got: %v", err)
	}
}

// TestFK_WebRoutes_CascadeOnGroupDelete verifies that deleting a group
// cascades to its web_routes (migration 0068, ON DELETE CASCADE).
func TestFK_WebRoutes_CascadeOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWebRoute(WebRoute{
		PathPrefix: "/pub/acme/",
		Access:     "public",
		Folder:     "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListWebRoutes("acme"); len(got) != 1 {
		t.Fatalf("seed: want 1 web_route, got %d", len(got))
	}

	if err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListWebRoutes("acme"); len(got) != 0 {
		t.Fatalf("after group delete: want 0 web_routes (CASCADE), got %d", len(got))
	}
}

// TestFK_RouteTokens_RejectsUnknownFolder verifies migration 0069:
// route_tokens.owner_folder → groups.folder rejects rows whose
// owner_folder has no parent group.
func TestFK_RouteTokens_RejectsUnknownFolder(t *testing.T) {
	s := openFKStore(t)
	raw := GenRouteToken()
	err := s.InsertRouteToken(raw, RouteToken{
		JID:         "web:ghost",
		OwnerFolder: "ghost-folder-with-no-group",
	})
	if err == nil {
		t.Fatal("expected FK violation, got nil")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") &&
		!strings.Contains(err.Error(), "constraint") {
		t.Fatalf("expected FK error, got: %v", err)
	}
}

// TestFK_RouteTokens_CascadeOnGroupDelete verifies that deleting a group
// cascades to its route_tokens (migration 0069, ON DELETE CASCADE).
func TestFK_RouteTokens_CascadeOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	raw := GenRouteToken()
	if err := s.InsertRouteToken(raw, RouteToken{
		JID:         "web:acme",
		OwnerFolder: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 1 {
		t.Fatalf("seed: want 1 route_token, got %d", len(got))
	}

	if err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 0 {
		t.Fatalf("after group delete: want 0 route_tokens (CASCADE), got %d", len(got))
	}
	if _, ok := s.LookupRouteToken(raw); ok {
		t.Fatal("after group delete: token still resolves (CASCADE didn't fire)")
	}
}

// TestFK_TaskRunLogs_CascadeOnTaskDelete verifies the pre-existing FK
// from migration 0011: task_run_logs.task_id → scheduled_tasks.id
// ON DELETE CASCADE. Pin the contract.
func TestFK_TaskRunLogs_CascadeOnTaskDelete(t *testing.T) {
	s := openFKStore(t)
	db := s.DB()
	_, err := db.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, status, created_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		"task-1", "system", "acme", "noop", "active",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, status)
		 VALUES (?, datetime('now'), 'ok')`,
		"task-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity check via direct count.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_run_logs WHERE task_id='task-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seed: want 1 run log, got %d", n)
	}
	if _, err := db.Exec(`DELETE FROM scheduled_tasks WHERE id='task-1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_run_logs WHERE task_id='task-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("after task delete: want 0 run logs (CASCADE), got %d", n)
	}
}

// TestFK_HistoryTablesStrandOnGroupDelete verifies that history tables
// (messages, audit_log, cost_log) are NOT cascade-deleted on group
// removal — per spec 5/36 they must strand. This pins the absence of
// a FK on these tables.
func TestFK_HistoryTablesStrandOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	db := s.DB()
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	// Seed a message and an audit_log entry for the group.
	if _, err := db.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
		 VALUES ('m1', 'acme', 'u', 'hi', datetime('now'))`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO audit_log (category, action, actor, folder, outcome)
		 VALUES ('mutation', 'group.test', 'system', 'acme', 'ok')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	// History strands by spec.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid='acme'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages stranded count: want 1, got %d", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE folder='acme' AND action='group.test'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit_log stranded count: want 1, got %d", n)
	}
}

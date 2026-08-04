package routd

import (
	"testing"

	"github.com/kronael/arizuko/store"
)

// TestMigration0015ProxydRoutesIgnoresLegacy proves routd.db gains proxyd_routes
// and that routd.Open does NOT read a sibling messages.db to fill it. The
// monolith→split copy belongs to `arizuko migrate-split` alone; routd opening
// the retired monolith on every boot was the last reason it stayed live.
func TestMigration0015ProxydRoutesIgnoresLegacy(t *testing.T) {
	dir := t.TempDir()

	// A pre-split messages.db sitting next to routd.db must be inert.
	msg, err := store.Open(dir) // store.Open == messages.db, runs store migrations
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	if err := seedProxydRoute(t, msg, "/panel/", "http://up"); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	msg.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer d.Close()

	routes, err := store.New(d.SQL()).AllProxydRoutes()
	if err != nil {
		t.Fatalf("read proxyd_routes from routd.db: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routd.Open read the retired messages.db: got %d proxyd_routes, want 0", len(routes))
	}
}

// TestMigration0024DropsAuthSessions proves the cookie-session table is gone
// from routd.db, even though a legacy messages.db still carries it.
func TestMigration0024DropsAuthSessions(t *testing.T) {
	dir := t.TempDir()

	msg, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	if _, err := msg.DB().Exec(
		`INSERT INTO auth_sessions (token_hash, user_sub, expires_at, created_at)
		 VALUES ('h','local:bob','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	msg.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer d.Close()

	var n int
	if err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_sessions'`,
	).Scan(&n); err != nil {
		t.Fatalf("probe sqlite_master: %v", err)
	}
	if n != 0 {
		t.Error("auth_sessions still present in routd.db after migration 0024")
	}
}

// TestMigration0015NoLegacyDB proves routd.Open is a clean no-op when there is
// no sibling messages.db (a fresh install) — the table exists, empty.
func TestMigration0015NoLegacyDB(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open on fresh dir: %v", err)
	}
	defer d.Close()
	routes, err := store.New(d.SQL()).AllProxydRoutes()
	if err != nil {
		t.Fatalf("read proxyd_routes: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("fresh proxyd_routes = %d rows, want 0", len(routes))
	}
}

// seedProxydRoute inserts one proxyd_routes row via raw SQL (store has no
// standalone insert helper outside the tx-aware proxyd path).
func seedProxydRoute(t *testing.T, s *store.Store, path, backend string) error {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO proxyd_routes (path, backend, auth) VALUES (?,?,'user')`,
		path, backend)
	return err
}

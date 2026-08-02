package routd

import (
	"testing"

	"github.com/kronael/arizuko/store"
)

// TestMigration0015ProxydRoutes proves routd.db gains proxyd_routes and that
// routd.Open backfills existing rows from a sibling messages.db (the pre-split
// source proxyd used to read). Guards the two-DB route-resolution straddle fix.
func TestMigration0015ProxydRoutes(t *testing.T) {
	dir := t.TempDir()

	// Seed a pre-split messages.db with a route.
	msg, err := store.Open(dir) // store.Open == messages.db, runs store migrations
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	if err := seedProxydRoute(t, msg, "/panel/", "http://up"); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	msg.Close()

	// routd.Open runs 0015 (schema) + copyLegacyProxydTables (backfill).
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer d.Close()

	rst := store.New(d.SQL())
	routes, err := rst.AllProxydRoutes()
	if err != nil {
		t.Fatalf("read proxyd_routes from routd.db: %v", err)
	}
	if len(routes) != 1 || routes[0].Path != "/panel/" {
		t.Fatalf("proxyd_routes not backfilled: %+v", routes)
	}

	// Idempotent: re-opening must not duplicate or error (INSERT OR IGNORE).
	d.Close()
	d2, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open (second): %v", err)
	}
	defer d2.Close()
	routes2, err := store.New(d2.SQL()).AllProxydRoutes()
	if err != nil {
		t.Fatalf("re-read proxyd_routes: %v", err)
	}
	if len(routes2) != 1 {
		t.Errorf("second open duplicated proxyd_routes: got %d rows, want 1", len(routes2))
	}
}

// TestMigration0024DropsAuthSessions proves the cookie-session table is gone
// from routd.db AND that the backfill no longer recreates rows for it, even
// though the legacy messages.db still carries the table.
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

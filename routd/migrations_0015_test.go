package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// TestMigration0015 proves routd.db gains auth_sessions + proxyd_routes and
// that routd.Open backfills existing rows from a sibling messages.db (the
// pre-split source proxyd used to read). Guards the two-DB login straddle fix:
// proxyd now resolves cookie sessions from routd.db.
func TestMigration0015AuthSessionsProxydRoutes(t *testing.T) {
	dir := t.TempDir()

	// Seed a pre-split messages.db with a live session, a user, and a route.
	msg, err := store.Open(dir) // store.Open == messages.db, runs store migrations
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	if err := msg.CreateAuthUser("local:bob", "bob", "", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	token := "live-refresh"
	if err := msg.CreateAuthSession(
		auth.HashToken(token), "local:bob", time.Now().Add(time.Hour),
	); err != nil {
		t.Fatalf("seed session: %v", err)
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

	// routd.db is where proxyd now reads: the session must resolve here.
	rst := store.New(d.SQL())
	sess, ok := rst.AuthSession(auth.HashToken(token))
	if !ok {
		t.Fatal("auth_sessions row not backfilled into routd.db")
	}
	if sess.UserSub != "local:bob" {
		t.Errorf("backfilled session user_sub = %q, want local:bob", sess.UserSub)
	}
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

// TestMigration0015NoLegacyDB proves routd.Open is a clean no-op when there is
// no sibling messages.db (a fresh install) — the tables exist, empty.
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
	if _, ok := store.New(d.SQL()).AuthSession("nope"); ok {
		t.Error("fresh auth_sessions returned a row")
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

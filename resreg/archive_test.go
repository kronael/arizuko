// Package resreg_test (external) so it can blank-import resreg/resources
// for its resource registrations — resreg/resources imports resreg, and an
// INTERNAL resreg test file cannot import a package that imports resreg
// back (a cycle at the test-binary level); the external test package
// convention is exactly the escape hatch for that.
package resreg_test

// Archive primitive tests — spec 5/8 "The full-instance archive". Uses
// store.Migrate's frozen-messages.db schema as the test fixture: per the
// same convention resreg/resources/resources_test.go documents, that schema
// is kept in lockstep with routd.db/onbod.db for most tables (store/
// migrations/0078 mirrors routd's route_tokens.kind, 0077 mirrors onbod's
// invites hash-at-rest).
//
// The messages/agent_cursor tests (Finding 4) are NOT here: store's frozen
// schema predates routd/migrations/0019 (link_context) and was never
// backported for it, so a full-column round-trip against this fixture would
// silently under-test. cmd/arizuko/archive_test.go uses the REAL routd.Open
// schema (the same bootstrap apply_test.go already established) and covers
// them there instead.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

func archiveTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestExportSnapshot_MatchesChecksum does NOT assert on any specific
// resource key being present in the manifest: other tests in this package
// binary (engine_test.go, openapi_test.go) call the package-private reset()
// between their own runs, and Go gives no ordering guarantee between an
// internal (package resreg) and external (package resreg_test) test file's
// registrations — so which resources are registered when THIS test runs is
// not something to depend on. What IS registry-order-independent, and is
// the actual thing worth proving, is that ExportSnapshot's one-read-tx path
// computes the SAME checksum Checksum() does against whatever the live
// registry holds at call time — both read it fresh, so they must always
// agree.
func TestExportSnapshot_MatchesChecksum(t *testing.T) {
	db := archiveTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	manifest, checksum, snapshotAt, err := resreg.ExportSnapshot(ctx, db, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if manifest == nil {
		t.Error("manifest nil")
	}
	if snapshotAt == "" {
		t.Error("snapshotAt empty")
	}
	want, err := resreg.Checksum(db, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if checksum != want {
		t.Errorf("checksum = %s, want %s (resreg.Checksum())", checksum, want)
	}
}

func TestRouteTokens_ArchiveRoundTrip_ExcludesPairing(t *testing.T) {
	db := archiveTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, kind)
		VALUES (x'aabbcc', 'web:atlas', 'atlas', '2026-01-01T00:00:00Z', 'route')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, kind)
		VALUES (x'ddeeff', 'telegram:user/1', 'atlas', '2026-01-01T00:00:00Z', 'pair')`); err != nil {
		t.Fatal(err)
	}
	rows, err := resreg.ExportRouteTokens(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("exported %d route_tokens, want 1 (pairing excluded)", len(rows))
	}
	if rows[0].TokenHash != "aabbcc" {
		t.Errorf("token_hash = %q, want aabbcc", rows[0].TokenHash)
	}

	dst := archiveTestDB(t)
	if _, err := dst.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	n, err := resreg.ImportRouteTokens(ctx, dst, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	var jid, kind string
	if err := dst.QueryRow("SELECT jid, kind FROM route_tokens WHERE hex(token_hash)='AABBCC' OR hex(token_hash)='aabbcc'").
		Scan(&jid, &kind); err != nil {
		t.Fatal(err)
	}
	if jid != "web:atlas" || kind != "route" {
		t.Errorf("restored row = jid=%q kind=%q", jid, kind)
	}
	count, err := resreg.CountRouteTokens(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountRouteTokens = %d, want 1", count)
	}
}

func TestRouteTokens_ImportIdempotent(t *testing.T) {
	db := archiveTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rows := []resreg.ArchiveRouteTokenRow{{TokenHash: "aabbcc", JID: "web:atlas", OwnerFolder: "atlas", CreatedAt: "2026-01-01T00:00:00Z"}}
	if _, err := resreg.ImportRouteTokens(ctx, db, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.ImportRouteTokens(ctx, db, rows); err != nil {
		t.Fatalf("re-import errored: %v", err)
	}
	n, _ := resreg.CountRouteTokens(ctx, db)
	if n != 1 {
		t.Errorf("count = %d, want 1 (no duplicate)", n)
	}
}

func TestInvites_ArchiveRoundTrip(t *testing.T) {
	db := archiveTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO invites(ref, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		VALUES ('ref1', 'atlas/*', 'user:admin', '2026-01-01T00:00:00Z', 5, 1)`); err != nil {
		t.Fatal(err)
	}
	rows, err := resreg.ExportInvites(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ref != "ref1" {
		t.Fatalf("exported invites = %+v", rows)
	}

	dst := archiveTestDB(t)
	n, err := resreg.ImportInvites(ctx, dst, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	var glob string
	var used int
	if err := dst.QueryRow("SELECT target_glob, used_count FROM invites WHERE ref='ref1'").Scan(&glob, &used); err != nil {
		t.Fatal(err)
	}
	if glob != "atlas/*" || used != 1 {
		t.Errorf("restored invite = glob=%q used=%d", glob, used)
	}
	count, err := resreg.CountInvites(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountInvites = %d, want 1", count)
	}
}

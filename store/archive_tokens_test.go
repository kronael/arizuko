package store_test

// The archive lane of the three tables whose VALUES ride the archive rather
// than the config manifest: route_tokens' verifier, invites' ref, and the
// onboarding admissions. `secrets`, the fourth, is exercised in
// secrets_test.go beside the pair it has always had here.
//
// The fixture is store's own frozen schema, which is the only chain carrying
// route_tokens (routd.db) and invites/onboarding (onbod.db) in one database —
// onbod embeds and migrates its chain from `package main`, so no test outside
// onbod can build the invites table any other way.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/store"
)

// archiveStore opens a *Store over the frozen schema. The archive lane is a
// method on the owner, so the test holds the owner; the raw pool comes back
// alongside it because the FIXTURES are raw inserts of columns no accessor
// writes (a pairing token, an admission mid-flight).
func archiveStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	db := dbtest.Open(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(db), db
}

func TestExportRouteTokensExcludesPairingAndImportRestoresTheVerifier(t *testing.T) {
	src, db := archiveStore(t)
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
	rows, err := src.ExportRouteTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("exported %d route_tokens, want 1 (pairing excluded)", len(rows))
	}
	if rows[0].TokenHash != "aabbcc" {
		t.Errorf("token_hash = %q, want aabbcc", rows[0].TokenHash)
	}

	dstSt, dst := archiveStore(t)
	if _, err := dst.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	n, err := dstSt.ImportRouteTokens(ctx, rows)
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
	count, err := dstSt.CountRouteTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountRouteTokens = %d, want 1", count)
	}
}

func TestImportRouteTokensIsIdempotent(t *testing.T) {
	st, db := archiveStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rows := []store.ArchiveRouteTokenRow{{TokenHash: "aabbcc", JID: "web:atlas", OwnerFolder: "atlas", CreatedAt: "2026-01-01T00:00:00Z"}}
	if _, err := st.ImportRouteTokens(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportRouteTokens(ctx, rows); err != nil {
		t.Fatalf("re-import errored: %v", err)
	}
	n, _ := st.CountRouteTokens(ctx)
	if n != 1 {
		t.Errorf("count = %d, want 1 (no duplicate)", n)
	}
}

func TestExportInvitesCarriesTheRefAndImportRestoresIt(t *testing.T) {
	src, db := archiveStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO invites(ref, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		VALUES ('ref1', 'atlas/*', 'user:admin', '2026-01-01T00:00:00Z', 5, 1)`); err != nil {
		t.Fatal(err)
	}
	rows, err := src.ExportInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ref != "ref1" {
		t.Fatalf("exported invites = %+v", rows)
	}

	dstSt, dst := archiveStore(t)
	n, err := dstSt.ImportInvites(ctx, rows)
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
	count, err := dstSt.CountInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountInvites = %d, want 1", count)
	}
}

// The onboarding lane had no test of its own in resreg — cmd/arizuko covered
// it only through the whole `archive apply` orchestration. The empty-string
// columns are the half worth pinning: an admission that never reached a state
// carries NULL, and a round trip that turned those into "" would make a
// pending admission look prompted.
func TestExportOnboardingRoundTripsTheEmptyTimestampsAsAbsent(t *testing.T) {
	src, db := archiveStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO onboarding(jid, status, created)
		VALUES ('telegram:user/7', 'pending', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO onboarding(jid, status, user_sub, gate, created, prompted_at, queued_at, admitted_at)
		VALUES ('telegram:user/8', 'admitted', 'user:x', 'g1', '2026-01-01T00:00:00Z',
		        '2026-01-02T00:00:00Z', '2026-01-03T00:00:00Z', '2026-01-04T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rows, err := src.ExportOnboarding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("exported %d onboarding rows, want 2", len(rows))
	}
	if rows[0].JID != "telegram:user/7" || rows[0].PromptedAt != "" || rows[0].UserSub != "" {
		t.Errorf("pending row = %+v, want empty user_sub/prompted_at", rows[0])
	}
	if rows[1].AdmittedAt != "2026-01-04T00:00:00Z" {
		t.Errorf("admitted row = %+v", rows[1])
	}

	dstSt, dst := archiveStore(t)
	n, err := dstSt.ImportOnboarding(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("imported %d, want 2", n)
	}
	var prompted sql.NullString
	if err := dst.QueryRow(`SELECT prompted_at FROM onboarding WHERE jid='telegram:user/7'`).Scan(&prompted); err != nil {
		t.Fatal(err)
	}
	if prompted.Valid {
		t.Errorf("prompted_at = %q, want NULL — an empty string is a value the "+
			"re-greet poll reads as already prompted", prompted.String)
	}
	count, err := dstSt.CountOnboarding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("CountOnboarding = %d, want 2", count)
	}
}

// A live admission must not be walked backwards by a restore: the import
// UPSERTs by jid and IGNOREs a jid the target already holds.
func TestImportOnboardingLeavesALiveAdmissionAlone(t *testing.T) {
	st, db := archiveStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO onboarding(jid, status, created, admitted_at)
		VALUES ('telegram:user/9', 'admitted', '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	n, err := st.ImportOnboarding(ctx, []store.ArchiveOnboardingRow{
		{JID: "telegram:user/9", Status: "pending", Created: "2025-01-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reported %d rows, want 1", n)
	}
	var status, admitted string
	if err := db.QueryRow(`SELECT status, COALESCE(admitted_at,'') FROM onboarding WHERE jid='telegram:user/9'`).
		Scan(&status, &admitted); err != nil {
		t.Fatal(err)
	}
	if status != "admitted" || admitted != "2026-02-01T00:00:00Z" {
		t.Errorf("live admission = status=%q admitted_at=%q, want the row untouched", status, admitted)
	}
}

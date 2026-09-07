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
	"fmt"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/objstore"
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
	src, _ := archiveStore(t)
	ctx := context.Background()
	seed := []store.ArchiveInviteRow{{
		Ref: "ref1", TargetGlob: "atlas/*", IssuedBySub: "user:admin",
		IssuedAt: "2026-01-01T00:00:00Z", MaxUses: 5, UsedCount: 1,
	}}
	if _, err := src.ImportInvites(ctx, seed); err != nil {
		t.Fatal(err)
	}
	rows, err := src.ExportInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ref != "ref1" {
		t.Fatalf("exported invites = %+v", rows)
	}

	dstSt, _ := archiveStore(t)
	n, err := dstSt.ImportInvites(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	back, err := dstSt.ExportInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].TargetGlob != "atlas/*" || back[0].UsedCount != 1 {
		t.Errorf("restored invite = %+v", back)
	}
	// An invite minted with no --expires carries no expires_at attribute at
	// all, and the round trip must not invent an empty one — absent is the
	// only null this model has.
	if back[0].ExpiresAt != "" {
		t.Errorf("expires_at = %q, want empty", back[0].ExpiresAt)
	}
	if err := invitesCarryNoExpiry(t, dstSt); err != nil {
		t.Error(err)
	}
	count, err := dstSt.CountInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountInvites = %d, want 1", count)
	}
}

// invitesCarryNoExpiry proves ABSENCE rather than emptiness: it asks the driver
// for the items that carry the attribute at all, which is the test a tombstone
// read performs and an empty string would pass.
func invitesCarryNoExpiry(t *testing.T, s *store.Store) error {
	t.Helper()
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: "invites", Partition: store.OwnerOnbod,
		Where: []objstore.Cmp{{Attr: "expires_at", Op: objstore.Present}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		return fmt.Errorf("%d invites carry an expires_at attribute, want 0 — an empty "+
			"string is a value a tombstone read still finds", len(items))
	}
	return nil
}

// The onboarding lane had no test of its own in resreg — cmd/arizuko covered
// it only through the whole `archive apply` orchestration. The empty-string
// columns are the half worth pinning: an admission that never reached a state
// carries NULL, and a round trip that turned those into "" would make a
// pending admission look prompted.
func TestExportOnboardingRoundTripsTheEmptyTimestampsAsAbsent(t *testing.T) {
	src, _ := archiveStore(t)
	ctx := context.Background()
	if _, err := src.ImportOnboarding(ctx, []store.ArchiveOnboardingRow{
		{JID: "telegram:user/7", Status: "pending", Created: "2026-01-01T00:00:00Z"},
		{
			JID: "telegram:user/8", Status: "admitted", UserSub: "user:x", Gate: "g1",
			Created: "2026-01-01T00:00:00Z", PromptedAt: "2026-01-02T00:00:00Z",
			QueuedAt: "2026-01-03T00:00:00Z", AdmittedAt: "2026-01-04T00:00:00Z",
		},
	}); err != nil {
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

	dstSt, _ := archiveStore(t)
	n, err := dstSt.ImportOnboarding(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("imported %d, want 2", n)
	}
	// ABSENT, not empty: the re-greet poll claims an admission by asking for the
	// ones with no prompted_at attribute, and an empty string is a value it
	// reads as already prompted.
	prompted, err := dstSt.Obj().Query(ctx, objstore.Query{
		Entity: "onboarding", Partition: store.OwnerOnbod,
		Where: []objstore.Cmp{{Attr: "prompted_at", Op: objstore.Present}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompted) != 1 || prompted[0].Key.Sort != "telegram:user/8" {
		t.Errorf("%d admissions carry prompted_at, want only telegram:user/8: %+v",
			len(prompted), prompted)
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
	st, _ := archiveStore(t)
	ctx := context.Background()
	live := store.ArchiveOnboardingRow{
		JID: "telegram:user/9", Status: "admitted",
		Created: "2026-01-01T00:00:00Z", AdmittedAt: "2026-02-01T00:00:00Z",
	}
	if _, err := st.ImportOnboarding(ctx, []store.ArchiveOnboardingRow{live}); err != nil {
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
	back, err := st.ExportOnboarding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0] != live {
		t.Errorf("live admission = %+v, want the row untouched at %+v", back, live)
	}
}

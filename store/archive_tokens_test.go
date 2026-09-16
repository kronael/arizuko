package store_test

// The archive lane of the three tables whose VALUES ride the archive rather
// than the config manifest: route_tokens' verifier, invites' ref, and the
// onboarding admissions. `secrets`, the fourth, is exercised in
// secrets_test.go beside the pair it has always had here.
//
// The fixture carries BOTH owners: route_tokens is routd's and
// invites/onboarding are onbod's, and the archive lane crosses them.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// archiveStore opens a *Store over both owners' tables. The archive lane is a
// method on the owner, so the test holds the owner.
func archiveStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestExportRouteTokensExcludesPairingAndImportRestoresTheVerifier(t *testing.T) {
	src := archiveStore(t)
	ctx := context.Background()
	seedArchiveGroup(t, src, "atlas")
	delivery := store.GenRouteToken()
	if err := src.InsertRouteToken(delivery, store.RouteToken{
		JID: "web:atlas", OwnerFolder: "atlas", TargetFolder: "atlas",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.IssuePairingLink(ctx, "telegram:user/1", "atlas"); err != nil {
		t.Fatal(err)
	}
	rows, err := src.ExportRouteTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("exported %d route_tokens, want 1 (pairing excluded)", len(rows))
	}
	if rows[0].TokenHash != store.TokenRef(delivery) {
		t.Errorf("token_hash = %q, want the bearer's digest %q",
			rows[0].TokenHash, store.TokenRef(delivery))
	}

	dstSt := archiveStore(t)
	seedArchiveGroup(t, dstSt, "atlas")
	n, err := dstSt.ImportRouteTokens(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	// The verifier travelled, so the ORIGINAL bearer resolves on the target —
	// and it resolves as a delivery token, which is kind-scoped.
	got, ok := dstSt.LookupRouteToken(delivery)
	if !ok {
		t.Fatal("the imported verifier does not resolve the bearer it was minted for")
	}
	if got.JID != "web:atlas" {
		t.Errorf("restored row = jid=%q, want web:atlas", got.JID)
	}
	count, err := dstSt.CountRouteTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CountRouteTokens = %d, want 1", count)
	}
}

// seedArchiveGroup lands the group a token names, through the writer every
// creating caller reaches.
func seedArchiveGroup(t *testing.T, s *store.Store, folder string) {
	t.Helper()
	if err := s.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatalf("seed group %s: %v", folder, err)
	}
}

func TestImportRouteTokensIsIdempotent(t *testing.T) {
	st := archiveStore(t)
	ctx := context.Background()
	seedArchiveGroup(t, st, "atlas")
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
	src := archiveStore(t)
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

	dstSt := archiveStore(t)
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
	src := archiveStore(t)
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

	dstSt := archiveStore(t)
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
	st := archiveStore(t)
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

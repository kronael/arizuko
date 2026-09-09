package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
)

func TestInviteCRUD(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	inv, token, err := s.CreateInvite("alice/", "github:alice", 3, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token len = %d, want 64", len(token))
	}
	if inv.Ref != TokenRef(token) {
		t.Errorf("Ref = %q, want TokenRef(token)", inv.Ref)
	}
	if inv.TargetGlob != "alice/" || inv.IssuedBySub != "github:alice" || inv.MaxUses != 3 || inv.UsedCount != 0 {
		t.Errorf("CreateInvite returned wrong values: %+v", inv)
	}
	if inv.IssuedAt.IsZero() {
		t.Error("IssuedAt should be set")
	}

	got, err := s.GetInvite(token)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if got.TargetGlob != "alice/" || got.IssuedBySub != "github:alice" {
		t.Errorf("GetInvite mismatch: %+v", got)
	}

	if _, _, err := s.CreateInvite("bob", "github:alice", 1, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, _, err := s.CreateInvite("eve", "github:eve", 1, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	all, err := s.ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("want 3 invites, got %d", len(all))
	}

	mine, err := s.ListInvites("github:alice")
	if err != nil {
		t.Fatalf("ListInvites(alice): %v", err)
	}
	if len(mine) != 2 {
		t.Errorf("want 2 invites for alice, got %d", len(mine))
	}

	if err := s.RevokeInviteByRef(inv.Ref); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, err := s.GetInvite(token); err == nil {
		t.Error("expected GetInvite to fail after revoke")
	}
}

func TestInviteCreateExpiresAt(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	exp := time.Now().Add(24 * time.Hour)
	_, token, err := s.CreateInvite("alice", "github:alice", 1, &exp)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	got, err := s.GetInvite(token)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt should be set")
	}
	if !got.ExpiresAt.Equal(exp.UTC().Truncate(time.Second)) {
		// RFC3339 truncates sub-second precision; truncate exp the same way for comparison.
		want := exp.UTC().Truncate(time.Second)
		if !got.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
		}
	}
}

func TestConsumeInviteHappyPath(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, token, _ := s.CreateInvite("alice", "github:alice", 2, nil)
	got, err := s.ConsumeInvite(token, "github:bob")
	if err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}
	if got.UsedCount != 1 {
		t.Errorf("used_count = %d, want 1", got.UsedCount)
	}

	rows, err := s.ListACL("github:bob")
	if err != nil {
		t.Fatalf("ListACL: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "admin" && r.Scope == "alice" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("acl rows for bob/alice = %d, want 1 (of %+v)", n, rows)
	}
}

// ConsumeInviteNoGrant (the split twin) increments used_count + audits but does
// NOT write the acl grant — acl is routd-OWNED, so onbod writes it to routd.db
// separately.
func TestConsumeInviteNoGrantSkipsACL(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, token, _ := s.CreateInvite("alice", "github:alice", 2, nil)
	got, err := s.ConsumeInviteNoGrant(token, "github:bob")
	if err != nil {
		t.Fatalf("ConsumeInviteNoGrant: %v", err)
	}
	if got.UsedCount != 1 {
		t.Errorf("used_count = %d, want 1", got.UsedCount)
	}

	// No acl row written by the no-grant path.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal=? AND scope=?`,
		"github:bob", "alice").Scan(&n)
	if n != 0 {
		t.Errorf("acl rows for bob/alice = %d, want 0 (no-grant)", n)
	}
}

func TestConsumeInviteExhausted(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, token, _ := s.CreateInvite("alice", "github:alice", 1, nil)
	if _, err := s.ConsumeInvite(token, "github:bob"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	_, err := s.ConsumeInvite(token, "github:eve")
	if !errors.Is(err, ErrInviteUnavailable) {
		t.Errorf("second consume: want ErrInviteUnavailable, got %v", err)
	}
}

func TestConsumeInviteExpired(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	_, token, _ := s.CreateInvite("alice", "github:alice", 1, &past)
	_, err := s.ConsumeInvite(token, "github:bob")
	if !errors.Is(err, ErrInviteUnavailable) {
		t.Errorf("expired consume: want ErrInviteUnavailable, got %v", err)
	}
}

func TestConsumeInviteUnknownToken(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, err := s.ConsumeInvite("doesnotexist", "github:bob")
	if !errors.Is(err, ErrInviteUnavailable) {
		t.Errorf("unknown token: want ErrInviteUnavailable, got %v", err)
	}
}

// ConsumeInvite must be atomic across concurrent goroutines: exactly
// max_uses succeed even with simulated parallel redemptions. Uses a
// real on-disk DB because :memory: opens a fresh schema per pooled
// connection.
func TestConsumeInviteAtomicConcurrent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const maxUses = 5
	const racers = 20
	_, token, _ := s.CreateInvite("alice", "github:alice", maxUses, nil)

	var (
		wg      sync.WaitGroup
		succeed int64
	)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			user := "user-" + string(rune('A'+i))
			// SQLite busy timeouts can surface as transient errors under
			// heavy contention; retry exactly once. The atomicity check
			// below still catches over-issuance.
			_, err := s.ConsumeInvite(token, user)
			if err != nil && !errors.Is(err, ErrInviteUnavailable) {
				_, err = s.ConsumeInvite(token, user)
			}
			if err == nil {
				atomic.AddInt64(&succeed, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Hard invariant: never more than max_uses succeed.
	if succeed > int64(maxUses) {
		t.Errorf("succeed = %d, exceeds max_uses %d", succeed, maxUses)
	}

	// DB state must agree with success count: no lost or extra increments.
	got, _ := s.GetInvite(token)
	if int64(got.UsedCount) != succeed {
		t.Errorf("DB used_count = %d, succeed = %d", got.UsedCount, succeed)
	}
}

// TestRestoreInviteRollsBackConsume: a single-use invite consumed (used_count→max)
// then RestoreInvite'd (decremented) is redeemable AGAIN — the split's rollback
// when the cross-DB acl grant fails, so an invite is never burned without a grant.
func TestRestoreInviteRollsBackConsume(t *testing.T) {
	s, _ := OpenMem()
	_, token, err := s.CreateInvite("acme", "github:alice", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeInviteNoGrant(token, "github:bob"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	// Exhausted now — a second consume must fail.
	if _, err := s.ConsumeInviteNoGrant(token, "github:carol"); err == nil {
		t.Fatal("expected exhausted invite to reject the second consume")
	}
	// Roll back the first consume (simulating a failed downstream grant).
	if err := s.RestoreInvite(token); err != nil {
		t.Fatalf("RestoreInvite: %v", err)
	}
	// Redeemable again — not burned without a grant.
	if _, err := s.ConsumeInviteNoGrant(token, "github:bob"); err != nil {
		t.Fatalf("consume after restore should succeed: %v", err)
	}
}

// TestBackfillInviteRefsCarriesLegacyRows: a pre-I1 plaintext invites table
// (renamed to invites_legacy by the SQL migration) is carried forward with
// its content intact, keyed by the hash instead of the raw token, and the
// legacy table is dropped. Simulates what store 0077 / onbod 0003 leave
// behind on an instance with existing invites.
func TestBackfillInviteRefsCarriesLegacyRows(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// OpenMem already ran BackfillInviteRefs once (a no-op — the fresh schema
	// has no invites_legacy). Recreate the pre-I1 shape with a live row to
	// simulate an existing instance, then backfill again.
	if _, err := s.db.Exec(`
		CREATE TABLE invites_legacy (
		  token TEXT PRIMARY KEY, target_glob TEXT NOT NULL, issued_by_sub TEXT NOT NULL,
		  issued_at TEXT NOT NULL, expires_at TEXT, max_uses INTEGER NOT NULL DEFAULT 1,
		  used_count INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO invites_legacy VALUES
		  ('plaintext-tok', 'alice', 'github:alice', '2026-01-01T00:00:00Z', NULL, 3, 1);
	`); err != nil {
		t.Fatal(err)
	}
	if err := BackfillInviteRefs(s.db); err != nil {
		t.Fatalf("BackfillInviteRefs: %v", err)
	}

	got, err := s.GetInvite("plaintext-tok")
	if err != nil {
		t.Fatalf("GetInvite after backfill: %v", err)
	}
	if got.Ref != TokenRef("plaintext-tok") || got.TargetGlob != "alice" || got.UsedCount != 1 || got.MaxUses != 3 {
		t.Errorf("carried row wrong: %+v", got)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='invites_legacy'`).Scan(&n)
	if n != 0 {
		t.Error("invites_legacy not dropped after backfill")
	}

	// Idempotent: a second call (as every later boot would make) is a no-op,
	// not an error, and the carried row is untouched.
	if err := BackfillInviteRefs(s.db); err != nil {
		t.Fatalf("second BackfillInviteRefs: %v", err)
	}
	if _, err := s.GetInvite("plaintext-tok"); err != nil {
		t.Fatalf("row lost across idempotent backfill: %v", err)
	}
}

// The redemption's COMPARE-AND-SWAP is what makes two concurrent redeemers
// share one use instead of both taking it, and this pins the LOSING side
// deterministically where TestConsumeInviteAtomicConcurrent pins it under
// contention. The condition is the counter value the loser's own read saw; a
// write built from it after somebody else moved the counter is REFUSED, which
// is the answer consumeInvite turns into ErrInviteUnavailable.
func TestUsedCountWriteRefusesAStaleCounter(t *testing.T) {
	s := openEntities(t)

	_, token, err := s.CreateInvite("alice", "github:alice", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The value a redeemer read one round trip ago.
	stale, err := s.GetInvite(token)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else redeems in the gap.
	if _, err := s.ConsumeInviteNoGrant(token, "github:bob"); err != nil {
		t.Fatalf("the competing redemption: %v", err)
	}
	key, err := inviteKey(stale.Ref)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := s.Obj().GuardedBatch(context.Background(), nil,
		[]objstore.Write{usedCountWrite(key, stale.UsedCount, stale.UsedCount+1)})
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Error("a write built from a counter value that has since moved was committed; " +
			"two redeemers would take one use and the invite would over-issue")
	}
	after, err := s.GetInvite(token)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedCount != 1 {
		t.Errorf("used_count = %d after one redemption and one refused write, want 1",
			after.UsedCount)
	}
}

// issued_at and expires_at are DECLARED 20 bytes wide: the list orders by one
// and the redemption compares the other, and both are byte comparisons (BUGS
// L97).
func TestInviteInstantsRefuseAnotherWidth(t *testing.T) {
	s := openEntities(t)

	_, token, err := s.CreateInvite("alice", "github:alice", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := inviteKey(TokenRef(token))
	if err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{attrInvIssuedAt, attrInvExpiresAt} {
		_, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
			Op: objstore.Update, Entity: Invites, Key: key,
			Attrs: map[string]string{attr: "2026-01-01"},
		}})
		if err == nil {
			t.Errorf("%s accepted a 10-byte value", attr)
			continue
		}
		if !strings.Contains(err.Error(), "declared ordered width is 20") {
			t.Errorf("%s refused with %v, want the ordered-width reason", attr, err)
		}
	}
}

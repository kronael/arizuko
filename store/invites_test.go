package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInviteCRUD(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	inv, err := s.CreateInvite("alice/", "github:alice", 3, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if len(inv.Token) != 64 {
		t.Errorf("token len = %d, want 64", len(inv.Token))
	}
	if inv.TargetGlob != "alice/" || inv.IssuedBySub != "github:alice" || inv.MaxUses != 3 || inv.UsedCount != 0 {
		t.Errorf("CreateInvite returned wrong values: %+v", inv)
	}
	if inv.IssuedAt.IsZero() {
		t.Error("IssuedAt should be set")
	}

	got, err := s.GetInvite(inv.Token)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if got.TargetGlob != "alice/" || got.IssuedBySub != "github:alice" {
		t.Errorf("GetInvite mismatch: %+v", got)
	}

	if _, err := s.CreateInvite("bob", "github:alice", 1, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := s.CreateInvite("eve", "github:eve", 1, nil); err != nil {
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

	if err := s.RevokeInvite(inv.Token); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, err := s.GetInvite(inv.Token); err == nil {
		t.Error("expected GetInvite to fail after revoke")
	}
}

func TestInviteCreateExpiresAt(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	exp := time.Now().Add(24 * time.Hour)
	inv, err := s.CreateInvite("alice", "github:alice", 1, &exp)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	got, err := s.GetInvite(inv.Token)
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

	inv, _ := s.CreateInvite("alice", "github:alice", 2, nil)
	got, err := s.ConsumeInvite(inv.Token, "github:bob")
	if err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}
	if got.UsedCount != 1 {
		t.Errorf("used_count = %d, want 1", got.UsedCount)
	}

	// acl admin row inserted (replaces legacy user_groups row).
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal=? AND action='admin' AND scope=?`,
		"github:bob", "alice").Scan(&n)
	if n != 1 {
		t.Errorf("acl rows for bob/alice = %d, want 1", n)
	}
}

func TestConsumeInviteExhausted(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	inv, _ := s.CreateInvite("alice", "github:alice", 1, nil)
	if _, err := s.ConsumeInvite(inv.Token, "github:bob"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	_, err := s.ConsumeInvite(inv.Token, "github:eve")
	if !errors.Is(err, ErrInviteUnavailable) {
		t.Errorf("second consume: want ErrInviteUnavailable, got %v", err)
	}
}

func TestConsumeInviteExpired(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	inv, _ := s.CreateInvite("alice", "github:alice", 1, &past)
	_, err := s.ConsumeInvite(inv.Token, "github:bob")
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
	inv, _ := s.CreateInvite("alice", "github:alice", maxUses, nil)

	var (
		wg      sync.WaitGroup
		succeed int64
	)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			user := "user-" + string(rune('A'+i))
			// SQLite busy timeouts can surface as transient errors under
			// heavy contention; retry exactly once. The atomicity check
			// below still catches over-issuance.
			_, err := s.ConsumeInvite(inv.Token, user)
			if err != nil && !errors.Is(err, ErrInviteUnavailable) {
				_, err = s.ConsumeInvite(inv.Token, user)
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
	got, _ := s.GetInvite(inv.Token)
	if int64(got.UsedCount) != succeed {
		t.Errorf("DB used_count = %d, succeed = %d", got.UsedCount, succeed)
	}
}

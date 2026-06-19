// Onboarding feature tests: invite lifecycle (create, consume, exhaust,
// revoke, expiry). Admission queue and group auto-create are onbod
// package-main — covered by onbod smoke tests.
package tests

import (
	"testing"
	"time"
)

func TestFeature_Onboarding(t *testing.T) {
	// CreateInvite returns a usable token; it appears in the issuer's list.
	t.Run("invite-create-list", func(t *testing.T) {
		s := mustMonolithDB(t)
		inv, err := s.CreateInvite("alice", "github:operator", 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if inv.Token == "" || inv.TargetGlob != "alice" {
			t.Fatalf("invite = %+v", inv)
		}
		list, err := s.ListInvites("github:operator")
		if err != nil || len(list) != 1 {
			t.Fatalf("list = %d err=%v, want 1", len(list), err)
		}
	})

	// Consuming a single-use invite increments used_count; a second consume fails.
	t.Run("invite-consume-exhausts", func(t *testing.T) {
		s := mustMonolithDB(t)
		inv, _ := s.CreateInvite("bob", "github:operator", 1, nil)
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:bob"); err != nil {
			t.Fatalf("first consume: %v", err)
		}
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:bob2"); err == nil {
			t.Fatal("single-use invite should be exhausted")
		}
	})

	// Multi-use invite can be consumed up to max_uses times.
	t.Run("invite-multi-use", func(t *testing.T) {
		s := mustMonolithDB(t)
		inv, _ := s.CreateInvite("team", "github:operator", 3, nil)
		for i, sub := range []string{"user:a", "user:b", "user:c"} {
			if _, err := s.ConsumeInviteNoGrant(inv.Token, sub); err != nil {
				t.Fatalf("consume %d: %v", i, err)
			}
		}
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:d"); err == nil {
			t.Fatal("invite should be exhausted after max_uses")
		}
	})

	// A revoked invite cannot be consumed.
	t.Run("invite-revoke", func(t *testing.T) {
		s := mustMonolithDB(t)
		inv, _ := s.CreateInvite("carol", "github:operator", 5, nil)
		if err := s.RevokeInvite(inv.Token); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:carol"); err == nil {
			t.Fatal("revoked invite should not consume")
		}
	})

	// An invite past its expiry cannot be consumed.
	t.Run("invite-expiry", func(t *testing.T) {
		s := mustMonolithDB(t)
		past := time.Now().Add(-time.Hour)
		inv, _ := s.CreateInvite("dave", "github:operator", 1, &past)
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:dave"); err == nil {
			t.Fatal("expired invite should not consume")
		}
	})

	// A future expiry allows consumption before expiry but not after.
	t.Run("invite-future-expiry-allows-then-blocks", func(t *testing.T) {
		s := mustMonolithDB(t)
		future := time.Now().Add(time.Hour)
		inv, _ := s.CreateInvite("eve", "github:operator", 2, &future)
		if _, err := s.ConsumeInviteNoGrant(inv.Token, "user:eve"); err != nil {
			t.Fatalf("future-expiry invite should be consumable now: %v", err)
		}
	})
}

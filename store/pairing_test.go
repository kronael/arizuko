package store

import (
	"errors"
	"testing"
	"time"
)

// seedPairing inserts a kind='pair' route token created at `created` and
// returns the raw token. Production mints through routd's issueRouteTokenTx
// (one minter for both kinds); this is the test-side twin so age can be dialled.
func seedPairing(t *testing.T, s *Store, jid, ownerFolder string, created time.Time) string {
	t.Helper()
	raw := GenRouteToken()
	if _, err := s.db.Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, kind) VALUES (?, ?, ?, ?, ?)`,
		TokenRefBytes(raw), jid, ownerFolder, created.Format(time.RFC3339Nano), RouteTokenKindPair,
	); err != nil {
		t.Fatalf("seed pairing token: %v", err)
	}
	return raw
}

func parentOf(t *testing.T, s *Store, child string) []string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT parent FROM acl_membership WHERE child = ? ORDER BY parent`, child)
	if err != nil {
		t.Fatalf("read parents: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan parent: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// A pairing token is not a delivery credential and a delivery token is not a
// pairing credential — resolution is kind-scoped in both directions.
func TestPairing_KindScopedBothWays(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")

	pair := seedPairing(t, s, "telegram:user/1", "acme", time.Now())
	if _, ok := s.LookupRouteToken(pair); ok {
		t.Error("pairing token resolved as a route token")
	}
	if _, err := s.RedeemPairing(pair, "google:alice"); err != nil {
		t.Fatalf("RedeemPairing: %v", err)
	}

	route := GenRouteToken()
	if err := s.InsertRouteToken(route, RouteToken{JID: "web:acme", OwnerFolder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeekPairing(route); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("PeekPairing(route token) = %v, want ErrPairingUnavailable", err)
	}
	if _, err := s.RedeemPairing(route, "google:alice"); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("RedeemPairing(route token) = %v, want ErrPairingUnavailable", err)
	}
	// The route token survived the failed redemption attempt.
	if _, ok := s.LookupRouteToken(route); !ok {
		t.Error("route token consumed by a pairing redemption")
	}
}

// A pairing row is not listed or revoked through the delivery-token surfaces.
func TestPairing_HiddenFromRouteTokenSurfaces(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	pair := seedPairing(t, s, "telegram:user/1", "acme", time.Now())

	if got := s.ListRouteTokens("acme"); len(got) != 0 {
		t.Errorf("ListRouteTokens leaked pairing rows: %+v", got)
	}
	if hit, err := s.RevokeRouteToken("telegram:user/1", "acme"); err != nil || hit {
		t.Errorf("RevokeRouteToken(pairing) = (%v, %v), want (false, nil)", hit, err)
	}
	if _, err := s.PeekPairing(pair); err != nil {
		t.Errorf("pairing token was destroyed by a route-token revoke: %v", err)
	}
}

// Peek is side-effect-free: an unfurl bot hitting the URL twice must not spend
// the pairing.
func TestPairing_PeekDoesNotConsume(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	raw := seedPairing(t, s, "telegram:user/1", "acme", time.Now())

	for i := range 2 {
		jid, err := s.PeekPairing(raw)
		if err != nil || jid != "telegram:user/1" {
			t.Fatalf("peek %d = (%q, %v)", i, jid, err)
		}
	}
	if _, err := s.RedeemPairing(raw, "google:alice"); err != nil {
		t.Fatalf("redeem after two peeks: %v", err)
	}
	if got := parentOf(t, s, "telegram:user/1"); len(got) != 1 || got[0] != "google:alice" {
		t.Fatalf("parents = %v, want [google:alice]", got)
	}
}

// Redemption is single-use: the token is gone afterwards and reports the same
// "link unavailable" as a token that never existed.
func TestPairing_RedeemConsumes(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	raw := seedPairing(t, s, "telegram:user/1", "acme", time.Now())

	if _, err := s.RedeemPairing(raw, "google:alice"); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if _, err := s.PeekPairing(raw); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("peek after redeem = %v, want ErrPairingUnavailable", err)
	}
	if _, err := s.RedeemPairing(GenRouteToken(), "google:alice"); !errors.Is(err, ErrPairingUnavailable) {
		t.Error("an unknown token must report ErrPairingUnavailable too")
	}
}

// An expired token fails with the SAME error as a missing one.
func TestPairing_Expired(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	old := seedPairing(t, s, "telegram:user/1", "acme", time.Now().Add(-PairingTTL-time.Second))

	if _, err := s.PeekPairing(old); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("peek expired = %v, want ErrPairingUnavailable", err)
	}
	if _, err := s.RedeemPairing(old, "google:alice"); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("redeem expired = %v, want ErrPairingUnavailable", err)
	}
	if got := parentOf(t, s, "telegram:user/1"); len(got) != 0 {
		t.Fatalf("expired token wrote an edge: %v", got)
	}
	// Just inside the window still works.
	fresh := seedPairing(t, s, "telegram:user/2", "acme", time.Now().Add(-PairingTTL+time.Minute))
	if _, err := s.RedeemPairing(fresh, "google:alice"); err != nil {
		t.Errorf("redeem inside TTL: %v", err)
	}
}

// One parent per channel identity: a second, different parent is refused; the
// same parent again is an idempotent no-op; a role membership does not block.
func TestPairing_OneParent(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")

	first := seedPairing(t, s, "telegram:user/1", "acme", time.Now())
	if _, err := s.RedeemPairing(first, "google:alice"); err != nil {
		t.Fatalf("first pairing: %v", err)
	}

	second := seedPairing(t, s, "telegram:user/1", "acme", time.Now())
	if _, err := s.RedeemPairing(second, "google:mallory"); !errors.Is(err, ErrPairingConflict) {
		t.Fatalf("second parent = %v, want ErrPairingConflict", err)
	}
	if got := parentOf(t, s, "telegram:user/1"); len(got) != 1 || got[0] != "google:alice" {
		t.Fatalf("parents after refused pairing = %v", got)
	}
	// The refused redemption did not consume its token.
	if _, err := s.PeekPairing(second); err != nil {
		t.Errorf("conflicting redemption consumed the token: %v", err)
	}

	// Re-pairing the SAME parent is a no-op that still succeeds.
	if _, err := s.RedeemPairing(second, "google:alice"); err != nil {
		t.Fatalf("re-pair same parent: %v", err)
	}
	if got := parentOf(t, s, "telegram:user/1"); len(got) != 1 {
		t.Fatalf("re-pair duplicated the edge: %v", got)
	}
}

// A role membership on the same JID does NOT block pairing (4e831f10): the
// one-parent rule excludes role: parents.
func TestPairing_RoleParentDoesNotBlock(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	if err := s.PutMembership("telegram:user/1", "role:member", "seed"); err != nil {
		t.Fatalf("seed role membership: %v", err)
	}

	raw := seedPairing(t, s, "telegram:user/1", "acme", time.Now())
	if _, err := s.RedeemPairing(raw, "google:alice"); err != nil {
		t.Fatalf("pairing blocked by a role membership: %v", err)
	}
	got := parentOf(t, s, "telegram:user/1")
	if len(got) != 2 || got[0] != "google:alice" || got[1] != "role:member" {
		t.Fatalf("parents = %v, want [google:alice role:member]", got)
	}
}

// The edge pairing writes is stamped added_by='pairing' — the scope unpair
// keys on so it can never reach role membership.
func TestPairing_EdgeStampedAddedBy(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")
	raw := seedPairing(t, s, "telegram:user/1", "acme", time.Now())
	if _, err := s.RedeemPairing(raw, "google:alice"); err != nil {
		t.Fatal(err)
	}
	var addedBy string
	if err := s.db.QueryRow(
		`SELECT COALESCE(added_by,'') FROM acl_membership WHERE child = ? AND parent = ?`,
		"telegram:user/1", "google:alice").Scan(&addedBy); err != nil {
		t.Fatal(err)
	}
	if addedBy != PairingAddedBy {
		t.Errorf("added_by = %q, want %q", addedBy, PairingAddedBy)
	}
}

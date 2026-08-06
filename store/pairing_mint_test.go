package store

// The pairing MINT (spec 5/31 § One mint, per-caller target resolver). Redemption
// lives in pairing_test.go; this file covers the half onbod's greeting needed and
// the NULL owner_folder that made it possible.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// IssuePairingLink is the one pairing minter, and both shapes of caller reach
// it: routd mints on resreg's mutation *sql.Tx, onbod's greeting on the raw
// *sql.DB handle it already writes acl_membership through. If the two ever drift
// into separate minters, one of these stops compiling.
func TestIssuePairingLink_MintsFromBothTxAndDB(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")

	onDB, err := IssuePairingLink(context.Background(), s.db, "telegram:user/1", "acme")
	if err != nil {
		t.Fatalf("mint on *sql.DB: %v", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	onTx, err := IssuePairingLink(context.Background(), tx, "telegram:user/2", "acme")
	if err != nil {
		t.Fatalf("mint on *sql.Tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for raw, want := range map[string]string{onDB: "telegram:user/1", onTx: "telegram:user/2"} {
		got, err := s.PeekPairing(raw)
		if err != nil || got != want {
			t.Errorf("PeekPairing = (%q, %v), want (%q, nil)", got, err, want)
		}
	}
}

// The raw token is never stored — only sha256(raw). Presenting the stored value
// must not redeem: it is a verifier, not a second bearer.
func TestIssuePairingLink_StoresOnlyTheHash(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolder(t, s, "acme")

	raw, err := IssuePairingLink(context.Background(), s.db, "telegram:user/1", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM route_tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == raw {
		t.Fatal("the bearer itself was stored")
	}
	if _, err := s.PeekPairing(string(stored)); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("the stored hash redeemed as if it were the bearer: %v", err)
	}
}

// onbod's greeting has no folder to name — the whole reason blocker 2 relaxed
// owner_folder. An empty ownerFolder must insert as NULL and stay redeemable;
// against the pre-migration schema this INSERT failed the NOT NULL constraint.
func TestIssuePairingLink_EmptyOwnerFolderStoresNull(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	raw, err := IssuePairingLink(context.Background(), s.db, "telegram:user/1", "")
	if err != nil {
		t.Fatalf("mint with no owner folder: %v", err)
	}
	var owner sql.NullString
	if err := s.db.QueryRow(`SELECT owner_folder FROM route_tokens`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Errorf("owner_folder = %q, want NULL", owner.String)
	}
	if jid, err := s.PeekPairing(raw); err != nil || jid != "telegram:user/1" {
		t.Errorf("an owner-less pairing did not resolve: (%q, %v)", jid, err)
	}
	if _, err := s.RedeemPairing(raw, "google:alice"); err != nil {
		t.Errorf("an owner-less pairing did not redeem: %v", err)
	}
}

// A NULL owner_folder stays invisible to the delivery surfaces, exactly like an
// owned pairing row. Nothing may treat it as a folder-less delivery token.
func TestIssuePairingLink_NullOwnerStaysHiddenFromDelivery(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	raw, err := IssuePairingLink(context.Background(), s.db, "telegram:user/1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LookupRouteToken(raw); ok {
		t.Error("an owner-less pairing resolved as a delivery bearer")
	}
	if got := s.ListRouteTokens(""); len(got) != 0 {
		t.Errorf(`ListRouteTokens("") returned %+v; a NULL owner must match no folder`, got)
	}
}

// jid is the one thing a pairing cannot be minted without: the token names the
// identity it binds, and a blank one would bind nothing.
func TestIssuePairingLink_RejectsEmptyJID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	// Seeded, so the only thing left to reject is the jid. Without it the FK on
	// owner_folder produces the error and the guard is never exercised.
	seedFolder(t, s, "acme")

	if _, err := IssuePairingLink(context.Background(), s.db, "", "acme"); err == nil {
		t.Fatal("minting a pairing for no jid must fail")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM route_tokens`).Scan(&n)
	if n != 0 {
		t.Errorf("a refused mint still wrote %d rows", n)
	}
}

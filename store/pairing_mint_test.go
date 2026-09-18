package store

// The pairing MINT (spec 5/31 § One mint, per-caller target resolver). Redemption
// lives in pairing_test.go; this file covers the half onbod's greeting needed and
// the NULL owner_folder that made it possible.

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// pairingMint is the one pairing minter, and both shapes of caller reach it:
// routd buffers its token on resreg's write set, onbod's greeting sends a batch
// of its own. If the two ever drift into separate minters, one of these stops
// compiling.
func TestIssuePairingLink_MintsIntoBothBatchAndSet(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "acme")

	alone, err := s.IssuePairingLink(context.Background(), "telegram:user/1", "acme")
	if err != nil {
		t.Fatalf("mint in a batch of its own: %v", err)
	}
	ws := &WriteSet{}
	buffered, err := s.IssuePairingLinkInto(ws, "telegram:user/2", "acme")
	if err != nil {
		t.Fatalf("mint onto a write set: %v", err)
	}
	if err := s.Apply(context.Background(), ws); err != nil {
		t.Fatal(err)
	}

	for raw, want := range map[string]string{alone: "telegram:user/1", buffered: "telegram:user/2"} {
		got, err := s.PeekPairing(raw)
		if err != nil || got != want {
			t.Errorf("PeekPairing = (%q, %v), want (%q, nil)", got, err, want)
		}
	}
}

// The raw token is never stored — only sha256(raw). Presenting the stored value
// must not redeem: it is a verifier, not a second bearer.
func TestIssuePairingLink_StoresOnlyTheHash(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "acme")

	raw, err := s.IssuePairingLink(context.Background(), "telegram:user/1", "acme")
	if err != nil {
		t.Fatal(err)
	}
	stored := onlyRouteTokenItem(t, s).Key.Sort
	if stored == raw {
		t.Fatal("the bearer itself was stored")
	}
	if _, err := s.PeekPairing(stored); !errors.Is(err, ErrPairingUnavailable) {
		t.Errorf("the stored hash redeemed as if it were the bearer: %v", err)
	}
}

// onbod's greeting has no folder to name — the whole reason blocker 2 relaxed
// owner_folder. An empty ownerFolder must write NO owner_folder attribute,
// which is the only null this model has, and stay redeemable.
func TestIssuePairingLink_EmptyOwnerFolderStoresNull(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	raw, err := s.IssuePairingLink(context.Background(), "telegram:user/1", "")
	if err != nil {
		t.Fatalf("mint with no owner folder: %v", err)
	}
	if owner, ok := onlyRouteTokenItem(t, s).Attrs[attrTokOwnerFolder]; ok {
		t.Errorf("owner_folder = %q, want the attribute absent", owner)
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
	s := openMem(t)
	defer s.Close()

	raw, err := s.IssuePairingLink(context.Background(), "telegram:user/1", "")
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
	s := openMem(t)
	defer s.Close()
	// Seeded so the owner folder is a real group and the only thing left to
	// reject is the jid.
	seedFolder(t, s, "acme")

	if _, err := s.IssuePairingLink(context.Background(), "", "acme"); err == nil {
		t.Fatal("minting a pairing for no jid must fail")
	}
	n, err := s.CountRouteTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a refused mint still wrote %d rows", n)
	}
}

// onlyRouteTokenItem is the one credential the store holds, for the two
// assertions that read what is AT REST rather than what a reader hands back.
func onlyRouteTokenItem(t *testing.T, s *Store) objstore.Item {
	t.Helper()
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: RouteTokens, Partition: OwnerRoutd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("route_tokens items = %d, want 1", len(items))
	}
	return items[0]
}

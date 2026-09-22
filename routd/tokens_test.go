package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// A pairing token is not a delivery credential: routd's own resolve/list/revoke
// surfaces are scoped to kind='route' (spec 5/31-identity-pairing).
func TestRouteTokenResolve_RejectsPairingKind(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}

	pair, err := db.store().IssuePairingLink(context.Background(), "telegram:user/1", "acme")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := resolveToken(db, pair); ok {
		t.Error("a pairing token resolved as a delivery token")
	}
	rows, err := db.ListRouteTokens("acme")
	if err != nil || len(rows) != 0 {
		t.Errorf("ListRouteTokens = (%+v, %v), want empty", rows, err)
	}
	if n := revokeTokens(t, db, "telegram:user/1", "acme"); n != 0 {
		t.Errorf("delivery revoke removed %d pairing rows, want 0", n)
	}

	// A delivery token still resolves. The two kinds have one minter each
	// (store.IssuePairingLink / store.MintRouteTokenInto) so a pairing can be
	// minted from onbod's process too; kind-scoping is what keeps them apart.
	ctx := context.Background()
	ws := &store.WriteSet{}
	route, err := db.store().MintRouteTokenInto(ctx, ws, "web:acme", "acme", "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(ctx, ws); err != nil {
		t.Fatal(err)
	}
	row, ok := resolveToken(db, route)
	if !ok || row.JID != "web:acme" || row.OwnerFolder != "acme" {
		t.Errorf("resolve(route) = (%q, %q, %v)", row.JID, row.OwnerFolder, ok)
	}
}

// TestRouteTokenTargetConflict: a JID names ONE chat, so a second mint may not
// bind it to a second folder. Without this guard RouteTokenTarget would read
// back two answers for one JID and deliver the payload to whichever row it saw
// first. A second mint at the SAME folder stays legal — spec 5/W permits several
// active tokens per JID.
func TestRouteTokenTargetConflict(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "a"})
	_ = db.PutGroup(core.Group{Folder: "a/b"})

	// folder a mints hook:a/b/c as (source b, suffix c).
	mintToken(t, db, "hook:a/b/c", "a", "a", "")
	// folder a/b would mint the SAME jid as (source c) — rejected. Minted
	// through the live store writer so the guard's error reaches the test.
	err = liveStore(db).InsertRouteToken(store.GenRouteToken(), store.RouteToken{
		JID: "hook:a/b/c", OwnerFolder: "a/b", TargetFolder: "a/b"})
	if !errors.Is(err, store.ErrRouteTokenTargetTaken) {
		t.Fatalf("second folder minted the same jid: err = %v, want ErrRouteTokenTargetTaken", err)
	}
	// A second token for the same folder is still legal.
	mintToken(t, db, "hook:a/b/c", "a", "a", "")
	if target := db.RouteTokenTarget("hook:a/b/c"); target != "a" {
		t.Fatalf("RouteTokenTarget = %q, want a", target)
	}
}

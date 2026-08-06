package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// A pairing token is not a delivery credential: routd's own resolve/list/revoke
// surfaces are scoped to kind='route' (spec 5/31).
func TestRouteTokenResolve_RejectsPairingKind(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := store.IssuePairingLink(context.Background(), tx, "telegram:user/1", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := db.ResolveRouteToken(pair); err != ErrNotFound {
		t.Errorf("ResolveRouteToken(pairing) err = %v, want ErrNotFound", err)
	}
	rows, err := db.ListRouteTokens("acme")
	if err != nil || len(rows) != 0 {
		t.Errorf("ListRouteTokens = (%+v, %v), want empty", rows, err)
	}
	n, err := db.RevokeRouteTokens("telegram:user/1", "acme")
	if err != nil || n != 0 {
		t.Errorf("RevokeRouteTokens(pairing) = (%d, %v), want (0, nil)", n, err)
	}

	// A delivery token still resolves. The two kinds now have one minter each
	// (store.IssuePairingLink / issueRouteTokenTx) so a pairing can be minted
	// from onbod's process too; kind-scoping is what keeps them apart.
	tx2, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	route, err := issueRouteTokenTx(context.Background(), tx2, "web:acme", "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	jid, owner, _, err := db.ResolveRouteToken(route)
	if err != nil || jid != "web:acme" || owner != "acme" {
		t.Errorf("ResolveRouteToken(route) = (%q, %q, %v)", jid, owner, err)
	}
}

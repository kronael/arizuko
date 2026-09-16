package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// Feature fixtures mint, resolve and revoke through store.Store — the SAME
// library the live callers use (webd/proxyd LookupRouteToken, dashd + the CLI
// InsertRouteToken/RevokeRouteToken). A fixture must never become a second
// implementation of the hashing scheme (BUGS L9).

func liveStore(d *routd.DB) *store.Store { return store.NewOver(d.Obj()) }

// mintToken issues a delivery token the way the CLI does.
func mintToken(t *testing.T, d *routd.DB, jid, owner, target, linkContext string) string {
	t.Helper()
	raw := store.GenRouteToken()
	err := liveStore(d).InsertRouteToken(raw, store.RouteToken{
		JID: jid, OwnerFolder: owner, TargetFolder: target,
		CreatedAt: time.Now(), Context: linkContext,
	})
	if err != nil {
		t.Fatalf("mint %s: %v", jid, err)
	}
	return raw
}

// resolveToken is the webd/proxyd resolution: raw token → row.
func resolveToken(d *routd.DB, raw string) (store.RouteToken, bool) {
	return liveStore(d).LookupRouteToken(raw)
}

// revokeTokens drops every delivery token for jid under owner and reports how
// many rows went. store.RevokeRouteToken deletes them all in one statement and
// reports only whether it hit, so the count comes from a pre-count.
func revokeTokens(t *testing.T, d *routd.DB, jid, owner string) int {
	t.Helper()
	want := 0
	for _, row := range liveStore(d).ListRouteTokens(owner) {
		if row.JID == jid {
			want++
		}
	}
	hit, err := liveStore(d).RevokeRouteToken(jid, owner)
	if err != nil {
		t.Fatalf("revoke %s: %v", jid, err)
	}
	if !hit {
		return 0
	}
	return want
}

// messageExists asks store.MessageByID, the live reader. routd.DB's
// MessageExists twin had no production caller and is gone (BUGS L14).
func messageExists(d *routd.DB, chatJID, id string) bool {
	_, ok := liveStore(d).MessageByID(chatJID, id)
	return ok
}

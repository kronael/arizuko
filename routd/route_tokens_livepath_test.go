package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/store"
)

// Test fixtures mint, resolve and revoke through store.Store — the SAME library
// the live callers use (webd/proxyd LookupRouteToken, dashd + the CLI
// InsertRouteToken/RevokeRouteToken). routd's own mint is the tx twin inside the
// resource, which resreg drives. These helpers exist so a fixture never becomes
// a THIRD implementation of the hashing scheme (BUGS L9).

func liveStore(d *DB) *store.Store { return store.NewOver(d.Obj()) }

// mintToken issues a delivery token the way the CLI does: GenRouteToken then
// InsertRouteToken. Returns the raw token, shown once.
func mintToken(t *testing.T, d *DB, jid, owner, target, linkContext string) string {
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
func resolveToken(d *DB, raw string) (store.RouteToken, bool) {
	return liveStore(d).LookupRouteToken(raw)
}

// revokeTokens drops every delivery token for jid under owner and reports how
// many rows went. store.RevokeRouteToken deletes them all in one statement and
// reports only whether it hit, so the count comes from a pre-count.
func revokeTokens(t *testing.T, d *DB, jid, owner string) int {
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

// putWebRoute seeds one rule through routd's own upsert, on a transaction of its
// own — the shape resreg gives that writer in production.
func putWebRoute(t *testing.T, d *DB, r WebRouteRow) {
	t.Helper()
	ws := &store.WriteSet{}
	if err := d.putWebRouteInto(ws, r); err != nil {
		t.Fatalf("put web route %s: %v", r.PathPrefix, err)
	}
	if err := d.store().Apply(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
}

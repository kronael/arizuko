package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestAlwaysProvesReviewerVersion(t *testing.T) {
	_, db, _, id := holdOnce(t, "like", map[string]any{"chatJid": "tg:1", "targetId": "156", "reaction": "yes"})
	ws := &store.WriteSet{}
	if _, err := db.resolveHoldInto(context.Background(), ws, id, PendingApproved, "google:op", "", true, errVerdictLost(id)); err != nil {
		t.Fatal(err)
	}
	if err := db.store().AddACLRow(core.ACLRow{Principal: "google:op", Action: "admin", Scope: "atlas", Effect: "deny"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(context.Background(), ws); !errors.Is(err, objstore.ErrScopeMoved) {
		t.Fatalf("verdict: %v", err)
	}
}

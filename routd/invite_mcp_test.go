package routd

import (
	"testing"
	"time"
)

// buildGatedFns.RevokeInvite enforces folder ownership: onbod's DELETE is
// token-only, so the closure first lists the folder's own invites and refuses a
// token it didn't issue — an agent can't revoke another folder's invite.
func TestBuildGatedFns_RevokeInvite_OwnershipGate(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	fo := &fakeOnbod{listed: []Invite{{Token: "mine", TargetGlob: "demo/", IssuedBySub: "agent:demo"}}}
	loop := NewLoop(db, &recRunner{}, LoopConfig{})
	loop.SetOnbodClient(fo)
	srv := NewServer(db, loop, nil, nil, 0, "")

	fns := srv.buildGatedFns(turnMCP{folder: "demo"})

	// Owned token → revoked.
	if err := fns.RevokeInvite("mine"); err != nil {
		t.Fatalf("RevokeInvite(owned): %v", err)
	}
	if len(fo.revoked) != 1 || fo.revoked[0] != "mine" {
		t.Fatalf("revoked = %v, want [mine]", fo.revoked)
	}

	// Token issued by another folder → rejected before the DELETE.
	err = fns.RevokeInvite("someone-elses")
	if err == nil {
		t.Fatalf("RevokeInvite(cross-folder) should be rejected")
	}
	if len(fo.revoked) != 1 {
		t.Fatalf("cross-folder revoke leaked to onbod: %v", fo.revoked)
	}
}

// CreateInvite + ListInvites federate to onbod and map to ipc.InviteInfo;
// ListInvites scopes to "agent:<folder>" so an agent sees only its own.
func TestBuildGatedFns_InviteCreateList(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	exp := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	fo := &fakeOnbod{listed: []Invite{{Token: "t1", TargetGlob: "demo/", MaxUses: 2, ExpiresAt: &exp}}}
	loop := NewLoop(db, &recRunner{}, LoopConfig{})
	loop.SetOnbodClient(fo)
	srv := NewServer(db, loop, nil, nil, 0, "")
	fns := srv.buildGatedFns(turnMCP{folder: "demo"})

	inv, err := fns.CreateInvite("demo/sub", "agent:demo", 1, &exp)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.TargetGlob != "demo/sub" || inv.IssuedBySub != "agent:demo" {
		t.Fatalf("CreateInvite mapped wrong: %+v", inv)
	}

	invs, err := fns.ListInvites("agent:demo")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invs) != 1 || invs[0].Token != "t1" || invs[0].ExpiresAt == nil {
		t.Fatalf("ListInvites mapped wrong: %+v", invs)
	}
}

// nil onbod (ONBOD_URL unset) → the invite closures return a clear error
// instead of nil-derefing.
func TestBuildGatedFns_InviteNoOnbod(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, NewLoop(db, &recRunner{}, LoopConfig{}), nil, nil, 0, "")
	fns := srv.buildGatedFns(turnMCP{folder: "demo"})

	if _, err := fns.CreateInvite("demo/", "agent:demo", 1, nil); err == nil {
		t.Fatalf("CreateInvite with nil onbod should error")
	}
	if _, err := fns.ListInvites("agent:demo"); err == nil {
		t.Fatalf("ListInvites with nil onbod should error")
	}
	if err := fns.RevokeInvite("x"); err == nil {
		t.Fatalf("RevokeInvite with nil onbod should error")
	}
}

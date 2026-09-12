package routd

import "testing"

// TestCmdApproveWorldOwnerResolvesOwnWorld is BUGS L271. A world creator holds
// `admin <world>/**` and nothing else — that is the one grant onbod.writeWorld
// writes. The chat verdict asked IsOperator, which needs `**`, and `admin
// atlas/**` does not cover `**`, so the only human who could answer a hold
// inside their own world was refused and every held call there waited until it
// expired.
func TestCmdApproveWorldOwnerResolvesOwnWorld(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	// Exactly the grant onbod.writeWorld writes for the creator of `atlas`.
	addACL(t, db, "u", "admin", "atlas/**", "allow")
	if err := db.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas/desk", Tool: "mcp:delete",
		ChatJID: "tg:held", Args: `{"target":"x"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if !steerOne(loop, "tg:1", "atlas/desk", "/approve a1") {
		t.Fatal("/approve not consumed")
	}
	row, _ := pendingActionRow(db, "a1")
	if row.Status != PendingApproved {
		t.Fatalf("status = %q, want approved; the ack was %q", row.Status, lastAck(t, dl))
	}
}

// TestCmdApproveOutsideOwnWorldRefused is the other half of L271: the folder the
// held call names is what the verdict is authorized against, so a world owner
// cannot answer a hold in a world they hold nothing on.
func TestCmdApproveOutsideOwnWorldRefused(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	addACL(t, db, "u", "admin", "atlas/**", "allow")
	if err := db.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "borg/desk", Tool: "mcp:delete", ChatJID: "tg:held",
	}); err != nil {
		t.Fatal(err)
	}
	if !steerOne(loop, "tg:1", "borg/desk", "/approve a1") {
		t.Fatal("/approve not consumed")
	}
	row, _ := pendingActionRow(db, "a1")
	if row.Status != PendingHeld {
		t.Fatalf("status = %q, want held — a verdict outside the reviewer's authority landed", row.Status)
	}
}

package routd

import (
	"slices"
	"testing"
)

// The T1 orphan: a follow-up steered into a live run lands in a DIFFERENT chat
// of the same folder (routd steers by folder), so the run-end clear must reach
// it. Before this the steered turn's indicator was owned by a submit_turn that
// never came and ran to the adapter backstop.
func TestTypingJIDsForRunCoversASteeredTurnInAnotherChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.PutTurnContext("parent", "demo", "", "slack:T/A/U", "u1", ""); err != nil {
		t.Fatalf("seed parent turn: %v", err)
	}
	if _, err := db.PutTurnContext("steered", "demo", "", "slack:T/B/U", "u2", ""); err != nil {
		t.Fatalf("seed steered turn: %v", err)
	}
	if err := db.SetTurnRunID("parent", "run-1"); err != nil {
		t.Fatalf("set parent run id: %v", err)
	}
	if err := db.SetTurnRunID("steered", "run-1"); err != nil {
		t.Fatalf("set steered run id: %v", err)
	}

	got := db.TypingJIDsForRun("run-1")
	for _, want := range []string{"slack:T/A/U", "slack:T/B/U"} {
		if !slices.Contains(got, want) {
			t.Errorf("TypingJIDsForRun = %v, missing %s", got, want)
		}
	}
}

// A delegated turn's indicator sits on the RETURN address, not the child
// folder's jid, so the run-end clear must resolve it the same way dispatch did.
func TestTypingJIDsForRunUsesTheReturnAddress(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.PutTurnContext("t", "child", "", "child", "u1", "slack:T/ORIGIN/U"); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if err := db.SetTurnRunID("t", "run-2"); err != nil {
		t.Fatalf("set run id: %v", err)
	}

	got := db.TypingJIDsForRun("run-2")
	if len(got) != 1 || got[0] != "slack:T/ORIGIN/U" {
		t.Fatalf("TypingJIDsForRun = %v, want [slack:T/ORIGIN/U]", got)
	}
}

// chatsOwnedBy always includes the turn's own indicator chat, even when the
// run reported no id (a transport failure before runed answered), and never
// tells an adapter about the same chat twice.
func TestChatsOwnedByAlwaysIncludesPrimaryAndDeduplicates(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.chatsOwnedBy("slack:T/A/U", ""); len(got) != 1 || got[0] != "slack:T/A/U" {
		t.Fatalf("no run id: got %v, want [slack:T/A/U]", got)
	}

	if _, err := db.PutTurnContext("t", "demo", "", "slack:T/A/U", "u1", ""); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if err := db.SetTurnRunID("t", "run-3"); err != nil {
		t.Fatalf("set run id: %v", err)
	}
	if got := db.chatsOwnedBy("slack:T/A/U", "run-3"); len(got) != 1 {
		t.Fatalf("got %v, want the chat listed once", got)
	}
}

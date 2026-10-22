package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// JIDRoutableToFolder authorizes a mention-only sub-folder to send to a chat
// whose default (verb="message") route resolves to an ancestor — without
// over-broadening to siblings.
func TestJIDRoutableToFolder_MentionOnlySubfolder(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	jid := "slack:T4PNSRSP7/channel/C0B4JMQ8X89"
	if _, err := db.AddRoute(core.Route{Seq: 10, Match: "chat_jid=" + jid + " verb=mention", Target: "atlas/support"}); err != nil {
		t.Fatalf("AddRoute mention: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 100, Match: "chat_jid=slack:*/channel/*", Target: "atlas#observe"}); err != nil {
		t.Fatalf("AddRoute wildcard: %v", err)
	}

	// The default resolution forces verb="message" → the wildcard wins → parent.
	if got := db.DefaultFolderForJID(jid); got != "atlas" {
		t.Fatalf("DefaultFolderForJID = %q, want atlas", got)
	}
	// The sub-folder is still a legitimate verb-agnostic route target.
	if !db.JIDRoutableToFolder(jid, "atlas/support") {
		t.Fatal("JIDRoutableToFolder(atlas/support) = false, want true")
	}
	// The parent is routable too (the wildcard targets it).
	if !db.JIDRoutableToFolder(jid, "atlas") {
		t.Fatal("JIDRoutableToFolder(atlas) = false, want true")
	}
	// A true sibling is NOT routable — the regression guard against over-broadening.
	if db.JIDRoutableToFolder(jid, "atlas/content") {
		t.Fatal("JIDRoutableToFolder(atlas/content) = true, want false")
	}
}

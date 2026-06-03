package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// JIDRoutableToFolder authorizes a mention-only sub-folder to send to a chat
// whose default (verb="message") route resolves to an ancestor — without
// over-broadening to siblings.
func TestJIDRoutableToFolder_MentionOnlySubfolder(t *testing.T) {
	s := openMem(t)
	jid := "slack:T4PNSRSP7/channel/C0B4JMQ8X89"
	if _, err := s.AddRoute(core.Route{Seq: 10, Match: "chat_jid=" + jid + " verb=mention", Target: "atlas/support"}); err != nil {
		t.Fatalf("AddRoute mention: %v", err)
	}
	if _, err := s.AddRoute(core.Route{Seq: 100, Match: "chat_jid=slack:*/channel/*", Target: "atlas#observe"}); err != nil {
		t.Fatalf("AddRoute wildcard: %v", err)
	}

	// The default resolution forces verb="message" → the wildcard wins → parent.
	if got := s.DefaultFolderForJID(jid); got != "atlas" {
		t.Fatalf("DefaultFolderForJID = %q, want atlas", got)
	}
	// The sub-folder is still a legitimate verb-agnostic route target.
	if !s.JIDRoutableToFolder(jid, "atlas/support") {
		t.Fatal("JIDRoutableToFolder(atlas/support) = false, want true")
	}
	// The parent is routable too (the wildcard targets it).
	if !s.JIDRoutableToFolder(jid, "atlas") {
		t.Fatal("JIDRoutableToFolder(atlas) = false, want true")
	}
	// A true sibling is NOT routable — the regression guard against over-broadening.
	if s.JIDRoutableToFolder(jid, "atlas/content") {
		t.Fatal("JIDRoutableToFolder(atlas/content) = true, want false")
	}
}

// An outbound bot row that stores its own platform id is matchable by
// IsBotMessageByID, so a later human reply promotes to verb=mention (spec 6/J).
func TestPutMessage_PlatformIDPromotable(t *testing.T) {
	s := openMem(t)
	ts := "1780300379.765170"
	if err := s.PutMessage(core.Message{
		ID: "mcp-test1", ChatJID: "slack:T/channel/C0B4", Sender: "atlas/support",
		Content: "hi", FromMe: true, BotMsg: true, PlatformID: ts,
	}); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	if !s.IsBotMessageByID(ts) {
		t.Fatal("IsBotMessageByID(platform TS) = false — reply-to-bot promotion would miss the tool reply")
	}
}

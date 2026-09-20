package store

import (
	"testing"
	"time"

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

// An outbound bot row carries a synthetic primary id, so the only handle on it
// the platform ever returns is `platform_id`. routd matches a reply's parent on
// `id OR platform_id` to promote it to verb=mention (spec 5/L); a row written
// without the column makes that lookup miss and the agent never re-attends.
func TestPutMessage_KeepsThePlatformID(t *testing.T) {
	s := openMem(t)
	ts := "1780300379.765170"
	if err := s.PutMessage(core.Message{
		ID: "mcp-test1", ChatJID: "slack:T/channel/C0B4", Sender: "atlas/support",
		Content: "hi", FromMe: true, BotMsg: true, PlatformID: ts, Timestamp: time.Now()}); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	m, ok := s.MessageByID("slack:T/channel/C0B4", "mcp-test1")
	if !ok {
		t.Fatal("the row did not land")
	}
	got := m.PlatformID
	if got != ts {
		t.Fatalf("platform_id = %q, want %q — the reply-to-bot lookup would miss the row", got, ts)
	}
}

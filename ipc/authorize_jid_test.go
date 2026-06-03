package ipc

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// A mention-only sub-folder must be allowed to reply to its chat even though
// the default (verb="message") resolution points at an ancestor; a true
// sibling that handles no route for the chat is still denied.
func TestAuthorizeJID_MentionOnlySubfolder(t *testing.T) {
	jid := "slack:T4PNSRSP7/channel/C0B4JMQ8X89"
	db := StoreFns{
		DefaultFolderForJID: func(string) string { return "atlas" },
		JIDRoutableToFolder: func(_, folder string) bool { return folder == "atlas/support" },
	}

	support := auth.Identity{Folder: "atlas/support", Tier: 1}
	if err := authorizeJID(support, "reply", jid, db); err != nil {
		t.Fatalf("authorizeJID(atlas/support, reply) = %v, want nil", err)
	}

	content := auth.Identity{Folder: "atlas/content", Tier: 1}
	if err := authorizeJID(content, "reply", jid, db); err == nil {
		t.Fatal("authorizeJID(atlas/content, reply) = nil, want forbidden")
	}
}

// recordOutbound stores the sent message's own platform id in PlatformID (not
// ReplyToID) so the reply-to-bot promotion can find it.
func TestRecordOutbound_StoresPlatformID(t *testing.T) {
	var got core.Message
	db := StoreFns{PutMessage: func(m core.Message) error { got = m; return nil }}

	recordOutbound(GatedFns{}, db, "slack:T/channel/C0B4", "hello", "1780300379.765170", "atlas/support")

	if got.PlatformID != "1780300379.765170" {
		t.Fatalf("recordOutbound PlatformID = %q, want the sent TS", got.PlatformID)
	}
	if got.ReplyToID != "" {
		t.Fatalf("recordOutbound ReplyToID = %q, want empty (own TS belongs in platform_id)", got.ReplyToID)
	}
}

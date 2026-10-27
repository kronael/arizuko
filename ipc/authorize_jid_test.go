package ipc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
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

	support := auth.Identity{Folder: "atlas/support"}
	if err := authorizeJID(support, "reply", jid, db); err != nil {
		t.Fatalf("authorizeJID(atlas/support, reply) = %v, want nil", err)
	}

	content := auth.Identity{Folder: "atlas/content"}
	if err := authorizeJID(content, "reply", jid, db); err == nil {
		t.Fatal("authorizeJID(atlas/content, reply) = nil, want forbidden")
	}
}

// A bare web:<folder> chat has no route row; an agent must still be able to
// reply to its own web-chat surface (the web-strict-1:1 binding), while a
// sibling agent that doesn't own that folder stays denied.
func TestAuthorizeJID_BareWebChat(t *testing.T) {
	db := StoreFns{DefaultFolderForJID: func(string) string { return "" }} // no route

	own := auth.Identity{Folder: "krons"}
	if err := authorizeJID(own, "reply", "web:krons", db); err != nil {
		t.Fatalf("authorizeJID(krons, reply, web:krons) = %v, want nil", err)
	}
	// subtree owner replies to a descendant's web surface
	parent := auth.Identity{Folder: "atlas"}
	if err := authorizeJID(parent, "reply", "web:atlas/strengths", db); err != nil {
		t.Fatalf("authorizeJID(atlas, reply, web:atlas/strengths) = %v, want nil", err)
	}
	sibling := auth.Identity{Folder: "mayai"}
	if err := authorizeJID(sibling, "reply", "web:krons", db); err == nil {
		t.Fatal("authorizeJID(mayai, reply, web:krons) = nil, want forbidden")
	}
}

// recordOutbound stores the sent message's own platform id in PlatformID (not
// ReplyToID) so the reply-to-bot promotion can find it.
func TestRecordOutbound_StoresPlatformID(t *testing.T) {
	var got core.Message
	db := StoreFns{PutOutbound: func(m core.Message, _ string) error { got = m; return nil }}

	recordOutbound(RoutdFns{}, db, "slack:T/channel/C0B4", "hello", "1780300379.765170", "atlas/support", "")

	if got.PlatformID != "1780300379.765170" {
		t.Fatalf("recordOutbound PlatformID = %q, want the sent TS", got.PlatformID)
	}
	if got.ReplyToID != "" {
		t.Fatalf("recordOutbound ReplyToID = %q, want empty (own TS belongs in platform_id)", got.ReplyToID)
	}
}

// `routed_to` names the FOLDER the turn ran in, and the folder feed is
// partitioned by it. A chat jid there makes the row invisible to every
// folder-scoped read — find_messages cannot see the reply, the per-folder counts
// understate it, and delegation refuses the jid as a target. The row already
// carries the chat as ChatJID, so a jid in this field names nothing new.
func TestRecordOutbound_RoutesToTheFolderNotTheChat(t *testing.T) {
	var got core.Message
	db := StoreFns{PutOutbound: func(m core.Message, _ string) error { got = m; return nil }}

	const folder = "atlas/support"
	const jid = "slack:T/channel/C0B4"
	recordOutbound(RoutdFns{}, db, jid, "hello", "1780300379.765170", folder, "")

	if got.RoutedTo != folder {
		t.Fatalf("recordOutbound RoutedTo = %q, want the turn's folder %q", got.RoutedTo, folder)
	}
	if got.ChatJID != jid {
		t.Fatalf("recordOutbound ChatJID = %q, want the chat %q", got.ChatJID, jid)
	}
}

// A suffixed web token belongs to the folder its MINT recorded, not to the
// folder its JID spells. `web:acme/support` is byte-identical to a plain token
// at a folder named `acme/support`, so reading the grammar lets an agent AT
// `acme/support` reply into `acme`'s chat — a descendant writing into its
// ancestor's conversation (BUGS L4). authorizeJID gates the agent's own
// reply/send, so this is a write, not a read.
func TestAuthorizeJID_SuffixedTokenBelongsToTheMintedFolder(t *testing.T) {
	jid := "web:acme/support" // minted at folder acme with jid_suffix=support
	db := StoreFns{
		DefaultFolderForJID: func(string) string { return "" }, // no route row
		RouteTokenTarget:    func(string) string { return "acme" },
	}

	owner := auth.Identity{Folder: "acme"}
	if err := authorizeJID(owner, "reply", jid, db); err != nil {
		t.Fatalf("authorizeJID(acme, reply, %s) = %v, want nil", jid, err)
	}

	// The folder the grammar names really exists and is a different tenant.
	impostor := auth.Identity{Folder: "acme/support"}
	if err := authorizeJID(impostor, "reply", jid, db); err == nil {
		t.Fatalf("authorizeJID(acme/support, reply, %s) = nil, want forbidden "+
			"(the grammar's reading, not the minted folder)", jid)
	}
}

// A token minted before target_folder existed records nothing, so the grammar
// stays the answer — an upgrade must not lock an agent out of its own chat.
func TestAuthorizeJID_PreColumnTokenKeepsTheGrammar(t *testing.T) {
	db := StoreFns{
		DefaultFolderForJID: func(string) string { return "" },
		RouteTokenTarget:    func(string) string { return "" },
	}
	own := auth.Identity{Folder: "acme/support"}
	if err := authorizeJID(own, "reply", "web:acme/support", db); err != nil {
		t.Fatalf("pre-column token: authorizeJID = %v, want nil", err)
	}
}

// A reply that ROOTS a platform thread lands in that thread, and the row has to
// say so. The MCP path stamped the turn's active topic, which is "" for a
// parent-channel turn, so an MCP-rooted thread recorded no thread at all: 4 of 9
// `mcp-` rows in one sloth thread carried an empty topic while the REST rows
// beside them said where they landed. threadHasBotMessage then missed the
// thread, the retry sweep had none to re-send into, and "where did this reply
// go" was unanswerable from the row (BUGS L166).
func TestRecordOutboundReply_RecordsTheThreadItRooted(t *testing.T) {
	var got core.Message
	db := StoreFns{PutOutbound: func(m core.Message, _ string) error { got = m; return nil }}

	const root = "1545760882322182276"
	recordOutboundReply(RoutdFns{}, db, "discord:g/channel/C1", "answer", "m-9", "sloth/desk", root, "", "reply")

	if got.Topic != root {
		t.Fatalf("Topic = %q, want the thread it rooted (%s)", got.Topic, root)
	}
}

// The delivery path resolves the destination. The source turn's topic must
// not move an explicit foreign-chat send into an unrelated thread.
func TestRecordOutboundReply_RecordsTheResolvedDestinationTopic(t *testing.T) {
	var got core.Message
	db := StoreFns{
		PutOutbound:  func(m core.Message, _ string) error { got = m; return nil },
		CurrentTopic: func(folder string) string { return "turn-topic" },
	}

	for _, topic := range []string{"", "destination-topic"} {
		recordOutboundReply(RoutdFns{}, db, "discord:g/channel/C1", "answer", "m-9", "sloth/desk", topic, "", "reply")
		if got.Topic != topic {
			t.Fatalf("Topic = %q, want resolved destination %q", got.Topic, topic)
		}
	}
}

// The PENDING row is the one the sweep reads, so it needs the thread most. A
// transient failure on a thread-rooting reply left the row with no thread, so
// the retry could not put it back and landed in the parent channel instead —
// on sloth the sweep re-posted a refused in-thread reply top-level, ten times
// (BUGS L169 link 2). The root is known before the send, so a failure has it.
func TestRecordOutboundFailure_KeepsTheThreadTheReplyMeantToRoot(t *testing.T) {
	var got core.Message
	var gotStatus string
	db := StoreFns{PutOutbound: func(m core.Message, status string) error {
		got, gotStatus = m, status
		return nil
	}}

	const root = "1545760882322182276"
	_ = recordOutboundFailure(RoutdFns{}, db, "discord:g/channel/C1", "answer", "sloth/desk",
		"parent-msg", root, "", "reply", true, errors.New("502 bad gateway"))

	if got.Topic != root {
		t.Fatalf("Topic = %q, want the thread it meant to root (%s) — the sweep cannot put it back without one", got.Topic, root)
	}
	if got.ReplyToID != "parent-msg" {
		t.Errorf("ReplyToID = %q, want the reply target so the sweep re-sends against it", got.ReplyToID)
	}
	if gotStatus != core.MessageStatusPending {
		t.Errorf("status = %q, want pending: a 502 is transient and the sweep owns the retry", gotStatus)
	}
}

// The row already decides who owns the retry; the agent is the reader that
// never got told. A `reply` answered 502, the sweep delivered the pending row
// 52 s later, and the agent — told only "failed" — sent the same text again six
// seconds after that, so the channel carried both copies (BUGS L165). The
// returned error must name the owner, and must still carry the platform's own
// message and sentinel so nothing downstream loses the classification.
func TestRecordOutboundFailure_TellsTheAgentWhoOwnsTheRetry(t *testing.T) {
	db := StoreFns{PutOutbound: func(core.Message, string) error { return nil }}

	transient := recordOutboundFailure(RoutdFns{}, db, "discord:g/channel/C1", "answer",
		"sloth/desk", "", "", "", "reply", true, errors.New("502 bad gateway"))
	if transient == nil {
		t.Fatal("transient failure returned nil, want the error the tool hands the agent")
	}
	if !strings.Contains(transient.Error(), "502 bad gateway") {
		t.Errorf("transient = %q, want the platform's own message kept", transient)
	}
	if !strings.Contains(transient.Error(), "do NOT send it again") {
		t.Errorf("transient = %q, want the agent told the router owns this retry", transient)
	}

	permanent := recordOutboundFailure(RoutdFns{}, db, "discord:g/channel/C1", "answer",
		"sloth/desk", "", "", "", "reply", true, fmt.Errorf("%w: chat gone", chanlib.ErrPermanent))
	if permanent == nil {
		t.Fatal("permanent failure returned nil, want the error the tool hands the agent")
	}
	if !strings.Contains(permanent.Error(), "dead-lettered") {
		t.Errorf("permanent = %q, want the agent told no retry follows", permanent)
	}
	if strings.Contains(permanent.Error(), "do NOT send it again") {
		t.Errorf("permanent = %q, want the dead letter NOT described as queued", permanent)
	}
	if !errors.Is(permanent, chanlib.ErrPermanent) {
		t.Errorf("errors.Is(permanent, ErrPermanent) = false; the wrap dropped the sentinel")
	}
}

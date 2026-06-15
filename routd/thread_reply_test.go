package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// threadTurn seeds a turn for the reply-into-thread tests: context + the
// trigger message id the dispatch path records via SetTurnTriggerMsg.
func threadTurn(t *testing.T, db *DB, turnID, folder, jid, trigger string) {
	t.Helper()
	if _, err := db.PutTurnContext(turnID, folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnTriggerMsg(turnID, trigger); err != nil {
		t.Fatal(err)
	}
}

// A REPLY in a multi-user chat roots a new platform thread on the trigger
// message: Send receives threadRoot = the trigger message id. A SEND to the
// same chat stays top-level (threadRoot empty) — the reply/send contract.
func TestReplyThreadsOnTriggerSendStaysTopLevel(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	jid := "discord:g1/ch-1"
	_ = db.SetChatIsGroup(jid, true)
	threadTurn(t, db, "t1", "demo", jid, "root-1")
	h := srv.Handler()

	doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: jid, Text: "answer"})
	if len(dl.sends) != 1 || dl.sends[0].threadRoot != "root-1" {
		t.Fatalf("reply threadRoot: got %+v, want root-1", dl.sends)
	}

	doJSONKey(t, h, "POST", "/v1/turns/t1/send", "k2",
		apiv1.ReplyRequest{JID: jid, Text: "broadcast"})
	if len(dl.sends) != 2 || dl.sends[1].threadRoot != "" {
		t.Fatalf("send threadRoot: got %+v, want empty", dl.sends)
	}
}

// No thread root when: the turn already runs in a thread (topic set → the
// adapter threads via threadID), the chat is a DM (is_group=0), or the
// group's thread_replies preference is off.
func TestReplyThreadRootSuppressed(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")

	// In-thread turn: topic set.
	jid := "discord:g1/ch-1"
	_ = db.SetChatIsGroup(jid, true)
	if _, err := db.PutTurnContext("t-th", "demo", "th-9", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	_ = db.SetTurnTriggerMsg("t-th", "root-1")
	tc, _ := db.GetTurnContext("t-th")
	if got := srv.replyThreadRoot(tc, jid); got != "" {
		t.Errorf("in-thread turn: threadRoot = %q, want empty", got)
	}

	// DM: chat not multi-user.
	dm := "discord:dm/d-1"
	threadTurn(t, db, "t-dm", "demo", dm, "root-2")
	tc, _ = db.GetTurnContext("t-dm")
	if got := srv.replyThreadRoot(tc, dm); got != "" {
		t.Errorf("dm turn: threadRoot = %q, want empty", got)
	}

	// Preference off beats the group-chat default.
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetThreadReplies("demo", false); err != nil {
		t.Fatal(err)
	}
	threadTurn(t, db, "t-off", "demo", jid, "root-3")
	tc, _ = db.GetTurnContext("t-off")
	if got := srv.replyThreadRoot(tc, jid); got != "" {
		t.Errorf("pref off: threadRoot = %q, want empty", got)
	}

	// Preference on beats the DM default.
	if err := db.SetThreadReplies("demo", true); err != nil {
		t.Fatal(err)
	}
	threadTurn(t, db, "t-on", "demo", dm, "root-4")
	tc, _ = db.GetTurnContext("t-on")
	if got := srv.replyThreadRoot(tc, dm); got != "root-4" {
		t.Errorf("pref on: threadRoot = %q, want root-4", got)
	}

	// Delegated turn: delivery redirected off the trigger chat.
	if _, err := db.PutTurnContext("t-del", "child", "", "web:child", "u1", "slack:T/C/ORIGIN"); err != nil {
		t.Fatal(err)
	}
	_ = db.SetTurnTriggerMsg("t-del", "fwd-1")
	tc, _ = db.GetTurnContext("t-del")
	if got := srv.replyThreadRoot(tc, returnTarget(tc, "web:child")); got != "" {
		t.Errorf("delegated turn: threadRoot = %q, want empty", got)
	}
}

// The in-process MCP twins agree with the REST face: reply carries the thread
// root, send does not.
func TestMcpDeliverReplyThreadsSendDoesNot(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	jid := "slack:T/channel/C1"
	_ = db.SetChatIsGroup(jid, true)
	threadTurn(t, db, "t1", "demo", jid, "1700000111.000100")

	if _, err := srv.mcpDeliver("t1", jid, "answer", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.mcpDeliver("t1", jid, "announce", "", false); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 2 {
		t.Fatalf("sends = %d", len(dl.sends))
	}
	if dl.sends[0].threadRoot != "1700000111.000100" {
		t.Errorf("mcp reply threadRoot = %q", dl.sends[0].threadRoot)
	}
	if dl.sends[1].threadRoot != "" {
		t.Errorf("mcp send threadRoot = %q, want empty", dl.sends[1].threadRoot)
	}
}

// A reply that starts a new thread (threadRoot set) stamps the bot message with
// Topic = threadRoot so that threadHasBotMessage finds it when subsequent
// in-thread messages arrive. Without this, the bot's first reply stores Topic=""
// and later thread participants fail to trigger the mention-promotion (spec 5/L).
func TestReplyStartsThreadSetsTopic(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	jid := "discord:g1/ch-1"
	_ = db.SetChatIsGroup(jid, true)
	threadTurn(t, db, "t1", "demo", jid, "root-1")
	h := srv.Handler()

	doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: jid, Text: "answer"})
	// The bot message row must have Topic = threadRoot.
	var topic string
	db.db.QueryRow("SELECT COALESCE(topic,'') FROM messages WHERE turn_id='t1' AND is_bot_message=1").Scan(&topic)
	if topic != "root-1" {
		t.Errorf("bot message topic = %q, want root-1", topic)
	}
	// threadHasBotMessage must now find it.
	if !srv.threadHasBotMessage(jid, "root-1") {
		t.Error("threadHasBotMessage returned false, want true")
	}
}

// threadHasBotMessage must find a bot reply whose topic="" but turn_id=thread_ts
// — the mcp-* path (ipc/recordOutbound) stores turn_id=trigger_id without
// stamping topic (unlike the REST path's deliverRow). The fix adds OR turn_id=?
// so the first bot reply in a thread is discoverable even when topic is empty.
func TestThreadHasBotMessage_MatchesByTurnID(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	jid := "slack:T/channel/C1"
	threadTS := "1700000100.000100"

	// Bot reply stored with topic="" and turn_id=threadTS (mcp-* path).
	if err := db.PutMessage(core.Message{
		ID:      "mcp-abc123",
		ChatJID: jid,
		TurnID:  threadTS,
		BotMsg:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if !srv.threadHasBotMessage(jid, threadTS) {
		t.Error("threadHasBotMessage must find bot reply matched by turn_id, got false")
	}
	// Must not fire for a different thread in the same chat.
	if srv.threadHasBotMessage(jid, "1700000999.000100") {
		t.Error("threadHasBotMessage must not match a different thread_ts")
	}
}

// A document delivered from an in-thread turn carries the topic as threadID —
// files thread like text (Document used to hardcode "" and never threaded).
func TestDocumentThreadsOnTopic(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	jid := "slack:T/channel/C1"
	if _, err := db.PutTurnContext("t1", "demo", "th-9", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	doJSONKey(t, srv.Handler(), "POST", "/v1/turns/t1/document", "k1",
		apiv1.DocumentRequest{JID: jid, Path: "/tmp/report.pdf", Name: "report.pdf"})
	if len(dl.docs) != 1 || dl.docs[0].threadID != "th-9" {
		t.Fatalf("document threadID: got %+v, want th-9", dl.docs)
	}
}

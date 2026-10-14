package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
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
// message: Send receives threadRoot = the trigger message id. The top-level
// `send` verb rides the MCP path (TestMcpDeliverReplyThreadsSendDoesNot).
func TestReplyThreadsOnTrigger(t *testing.T) {
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

	srv.appendAndDeliver("t1", jid, "answer")
	if len(dl.sends) != 1 || dl.sends[0].threadRoot != "root-1" {
		t.Fatalf("reply threadRoot: got %+v, want root-1", dl.sends)
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
	bFalse := false
	if err := setThreadReplies(db, "demo", &bFalse); err != nil {
		t.Fatal(err)
	}
	threadTurn(t, db, "t-off", "demo", jid, "root-3")
	tc, _ = db.GetTurnContext("t-off")
	if got := srv.replyThreadRoot(tc, jid); got != "" {
		t.Errorf("pref off: threadRoot = %q, want empty", got)
	}

	// Preference on beats the DM default.
	bTrue := true
	if err := setThreadReplies(db, "demo", &bTrue); err != nil {
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

// The in-process MCP reply/send agree with the prose-result path: reply
// carries the thread root, send does not.
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

	if _, _, err := srv.mcpDeliver("t1", jid, "answer", "", "mcp-1", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.mcpDeliver("t1", jid, "announce", "", "mcp-2", false); err != nil {
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

// A reply that starts a thread records the destination topic for thread reads.
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

	srv.appendAndDeliver("t1", jid, "answer")
	// The bot message row must have Topic = threadRoot.
	frames, err := db.store().TurnFrames(jid, "t1", "", 10)
	if err != nil || len(frames) == 0 {
		t.Fatalf("the bot row did not land: %d frames, %v", len(frames), err)
	}
	topic := frames[0].Topic
	if topic != "root-1" {
		t.Errorf("bot message topic = %q, want root-1", topic)
	}
}

// thread_replies=false suppresses new thread creation (threadRoot) but NOT
// continuation of an existing thread (threadID). A reply triggered from within
// a thread must stay in that thread so the person who asked can see the answer.
func TestReplyThreadPrefOffKeepsThreadID(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	jid := "slack:T/channel/C1"
	_ = db.SetChatIsGroup(jid, true)
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatal(err)
	}
	atlFalse := false
	if err := setThreadReplies(db, "atlas", &atlFalse); err != nil {
		t.Fatal(err)
	}
	// Turn is IN a thread: topic = existing thread_ts.
	if _, err := db.PutTurnContext("t1", "atlas", "1700000100.000100", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	_ = db.SetTurnTriggerMsg("t1", "1700000200.000200")

	srv.appendAndDeliver("t1", jid, "answer")
	if len(dl.sends) != 1 {
		t.Fatalf("sends = %d, want 1", len(dl.sends))
	}
	if dl.sends[0].threadRoot != "" {
		t.Errorf("threadRoot = %q, want empty (thread_replies=false)", dl.sends[0].threadRoot)
	}
	if dl.sends[0].threadID != "1700000100.000100" {
		t.Errorf("threadID = %q, want 1700000100.000100 (in-thread trigger stays in thread)", dl.sends[0].threadID)
	}
}

// A bot answer in a human thread does not address later ambient messages.
func TestHumanThreadParticipationDoesNotPromote(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	h := srv.Handler()
	jid := "discord:guild1/channel1"
	_ = db.SetChatIsGroup(jid, true)

	// Seed a bot message in the thread (topic = thread channel ID).
	threadID := "1234567890123456789"
	if err := db.PutMessage(core.Message{
		ID:      "bot-reply-1",
		ChatJID: jid,
		Topic:   threadID,
		BotMsg:  true, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// User types in the thread without using the reply arrow → ReplyTo = "".
	in := chanlib.InboundMsg{
		ID:      "user-msg-1",
		ChatJID: jid,
		Sender:  "discord:user/u1",
		Content: "hello",
		Topic:   threadID,
		ReplyTo: "", // no explicit reply reference
		IsGroup: true,
	}
	rec := doJSON(t, h, "POST", "/v1/messages", "", in)
	if rec.Code != 200 {
		t.Fatalf("inbound status %d", rec.Code)
	}

	seen, _ := db.MessageByID(jid, "user-msg-1")
	verb := seen.Verb
	if verb != "message" {
		t.Errorf("verb = %q, want message (human thread participation)", verb)
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
	if _, err := srv.mcpAppendDoc("t1", jid, "/tmp/report.pdf", "report.pdf", "", "", "", "mcp-3"); err != nil {
		t.Fatal(err)
	}
	if len(dl.docs) != 1 || dl.docs[0].threadID != "th-9" {
		t.Fatalf("document threadID: got %+v, want th-9", dl.docs)
	}
}

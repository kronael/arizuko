package routd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// holdOneCall arms a hold rule on the folder and suspends one `mcp:like` call
// through the REAL gate, returning the pending id.
func holdOneCall(t *testing.T, db *DB, srv *Server, folder, chatJID string) string {
	t.Helper()
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:" + folder, Action: auth.HoldPrefix + "mcp:like",
		Scope: folder, Effect: auth.EffectHoldAsync,
	}); err != nil {
		t.Fatalf("arm the hold rule: %v", err)
	}
	gate := srv.holdGate(turnMCP{folder: folder, chatJID: chatJID})
	if gate == nil {
		t.Fatal("a normal folder turn must get a gate")
	}
	id, held := gate("mcp:like", map[string]any{"platform_id": "p1", "emoji": "👍"})
	if !held || id == "" {
		t.Fatalf("the hold rule must suspend the call (held=%v id=%q)", held, id)
	}
	return id
}

// approveInChat runs the chat `/approve <id>` command — the same
// cmdResolveHold both the typed command and the dashboard button reach.
func approveInChat(t *testing.T, db *DB, srv *Server, chatJID, folder, id string) {
	t.Helper()
	if err := addMembership(db, "google:op", "role:operator", "test"); err != nil {
		t.Fatalf("make the reviewer an operator: %v", err)
	}
	srv.loop.cmdResolveHold(chatJID, folder, id, "google:op", PendingApproved, false)
	p, ok := pendingActionRow(db, id)
	if !ok || p.Status != PendingApproved {
		t.Fatalf("the verdict did not commit: %+v ok=%v", p, ok)
	}
}

// TestApproveReIssuesHeldCallInTheCursorSecond is BUGS L97 end to end.
//
// The operator types /approve, the verdict commits, and the re-issue trigger is
// written with nanosecond precision. The chat's cursor is a platform timestamp,
// which every adapter truncates to the second — so the trigger and the cursor
// share a second, and only a fixed-width layout keeps the trigger on the right
// side of `timestamp > agent_cursor`. Under a fraction-stripping layout the
// dispatch scan steps over it and the held call is never re-issued.
func TestApproveReIssuesHeldCallInTheCursorSecond(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Match: "platform=slack", Target: "atlas"}}})

	id := holdOneCall(t, db, srv, "atlas", chatJID)
	approveInChat(t, db, srv, chatJID, "atlas", id)

	// The trigger row the approval wrote.
	trigger := triggerRow(t, db, chatJID, id)

	// The adapter's half: a platform timestamp in the trigger's own second,
	// stored as the chat's cursor. This is the collision L97 reports, and it is
	// the normal case — the verdict and the platform row are the same second.
	cursorAt := trigger.Timestamp.Truncate(time.Second)
	// The cursor is a whole message position now, so it names the adapter's row
	// at that second rather than the bare second. The trigger carries a fraction
	// of the same second, so it sorts after ANY position at cursorAt whatever the
	// id — which is the guarantee this test is about, made stronger.
	if err := db.SetAgentCursor(chatJID, store.MsgStamp(core.TS(cursorAt), "platform-row")); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	// The dispatch scan must see the trigger across that cursor.
	msgs, err := db.NewMessagesForChat(chatJID, db.GetAgentCursor(chatJID), 100)
	if err != nil {
		t.Fatalf("messages since cursor: %v", err)
	}
	if !containsID(msgs, trigger.ID) {
		t.Fatalf("the re-issue trigger is invisible to the dispatch feed: "+
			"trigger %q vs cursor %q", core.TS(trigger.Timestamp), db.GetAgentCursor(chatJID))
	}

	// And the turn actually runs with the re-issue instruction in its batch.
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !strings.Contains(runner.gotBatch, "re-issue mcp:like") {
		t.Fatalf("the agent's batch carries no re-issue instruction: %q", runner.gotBatch)
	}

	// The re-issued call passes the gate exactly once — the approval is spent.
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: chatJID})
	if _, held := gate("mcp:like", map[string]any{"platform_id": "p1", "emoji": "👍"}); held {
		t.Fatal("the approved re-issue must run, not be held again")
	}
}

// TestPutMessageStoresAFixedWidthTimestamp pins the writer the cursor is
// compared against. A whole second is the case that gives a stripping layout
// away, and it is the common one: every adapter truncates a platform timestamp
// to the second before handing the row to routd.
func TestPutMessageStoresAFixedWidthTimestamp(t *testing.T) {
	db := memDB(t)
	whole := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC)
	if err := db.PutMessage(core.Message{
		ID: "m-whole", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: whole, Verb: "message",
	}); err != nil {
		t.Fatalf("put message: %v", err)
	}
	got := storedInstant(t, db, "slack:T/C/U", "m-whole")
	if want := "2026-08-26T19:06:41.000000000Z"; got != want {
		t.Fatalf("stored %q, want %q — a stored second must carry its zero fraction, "+
			"or it sorts after every fractional row of the same second", got, want)
	}
}

// TestPollOnceDoesNotSkipTheChatHoldingATrigger covers the other gate on the
// same second: pollOnce feeds from the global minimum cursor and skips any chat
// already current on its own, comparing the batch tail against that cursor as
// text. The trigger is the tail, so a stripped fraction makes the chat look
// current and it is never enqueued.
func TestPollOnceDoesNotSkipTheChatHoldingATrigger(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Match: "platform=slack", Target: "atlas"}}})

	id := holdOneCall(t, db, srv, "atlas", chatJID)
	approveInChat(t, db, srv, chatJID, "atlas", id)

	trigger := triggerRow(t, db, chatJID, id)
	if err := db.SetAgentCursor(chatJID, store.MsgStamp(core.TS(trigger.Timestamp.Truncate(time.Second)), "platform-row")); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	// pollOnce enqueues; the queue is stopped in this harness, so drive the
	// dispatch the enqueue would have reached and assert the turn ran.
	srv.loop.pollOnce()
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.gotTurn != trigger.ID {
		t.Fatalf("the poll skipped the chat holding the trigger: turn_id=%q want %q",
			runner.gotTurn, trigger.ID)
	}
}

// TestMigration0044NormalisesTheMessageRows pins the half of L97 that lives in
// the migration chain: the ROWS a cursor is compared against.
//
// The cursors themselves are no longer columns — `chats` and `sessions` are
// objstore entities — so the migration pads the rows and `arizuko migrate-hot`
// pads the cursors as it carries them, refusing to run until this has. The
// cursor half is proved there, over the same legacy state; what is proved here
// is that this half still happens, and that it leaves alone the one shape it
// must not touch.
func TestMigration0044NormalisesTheMessageRows(t *testing.T) {
	db := memDB(t)
	const chatJID = "slack:T/C/U"

	// `messages` is an entity here, and 0044 is a FROZEN migration that still
	// runs on a database whose rows have not been moved yet — so the fixture
	// puts the columns back to have something for it to normalise.
	if _, err := db.SQL().Exec(legacyMessagesTable); err != nil {
		t.Fatalf("put the pre-move columns back: %v", err)
	}

	// The state a pre-migration instance holds: a nanosecond row and a
	// whole-second row in the same second.
	if _, err := db.SQL().Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES ('hitl-legacy', ?, 'google:op', '', 'Approved held call',
		    '2026-08-26T19:06:41.882458214Z', 0, 0, '', '', '', '', '', 'message', '', '', 0, 'sent', '')`,
		chatJID,
	); err != nil {
		t.Fatalf("seed legacy trigger: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES ('whole-second', ?, 'u1', '', 'same second, no fraction',
		    '2026-08-26T19:06:41Z', 0, 0, '', '', '', '', '', 'message', '', '', 0, 'sent', '')`,
		chatJID,
	); err != nil {
		t.Fatalf("seed whole-second row: %v", err)
	}
	// A row nothing in the codebase can produce: it must survive untouched
	// rather than have a zone invented for it.
	if _, err := db.SQL().Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES ('hand-written', ?, 'u1', '', 'poked in by hand',
		    '2026-04-02 12:38:47', 0, 0, '', '', '', '', '', 'message', '', '', 0, 'sent', '')`,
		chatJID,
	); err != nil {
		t.Fatalf("seed malformed row: %v", err)
	}

	applyMigration(t, db, "migrations/0044-fixed-width-timestamps.sql")

	for id, want := range map[string]string{
		"hitl-legacy":  "2026-08-26T19:06:41.882458214Z",
		"whole-second": "2026-08-26T19:06:41.000000000Z",
		"hand-written": "2026-04-02 12:38:47",
	} {
		var got string
		if err := db.SQL().QueryRow(
			"SELECT timestamp FROM messages WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}

	// Idempotent: a second run must not move anything.
	applyMigration(t, db, "migrations/0044-fixed-width-timestamps.sql")
	var ts string
	if err := db.SQL().QueryRow(
		"SELECT timestamp FROM messages WHERE id = 'hitl-legacy'").Scan(&ts); err != nil {
		t.Fatalf("read after rerun: %v", err)
	}
	if ts != "2026-08-26T19:06:41.882458214Z" {
		t.Errorf("a second run rewrote a normalised row to %q", ts)
	}
}

// storedInstant reads the instant a message was STORED at — the lead of its
// position, as the bytes on disk carry it. Reading it back through core.TS
// would render the value a second time and hide the defect under test.
func storedInstant(t *testing.T, db *DB, chatJID, id string) string {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT sort FROM messages WHERE part = ?`, chatJID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sort string
		if err := rows.Scan(&sort); err != nil {
			t.Fatal(err)
		}
		if at, got, ok := store.MsgCoord(sort); ok && got == id {
			return at
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("no message %s in %s", id, chatJID)
	return ""
}

func containsID(msgs []core.Message, id string) bool {
	for _, m := range msgs {
		if m.ID == id {
			return true
		}
	}
	return false
}

// triggerRow reads the re-issue trigger a verdict wrote for a pending id. The
// row id is core.MsgID("hitl-"+id), so the pending id is its prefix — and the
// row is in the chat the verdict was given in, which is its partition.
func triggerRow(t *testing.T, db *DB, chatJID, pendingID string) core.Message {
	t.Helper()
	msgs, err := db.MessagesSince(chatJID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "hitl-"+pendingID+"-") {
			return m
		}
	}
	t.Fatalf("no re-issue trigger row for %s in %s", pendingID, chatJID)
	return core.Message{}
}

// applyMigration runs a SHIPPED migration file. Reading the file rather than
// inlining its SQL is what keeps the test from passing against an empty one.
func applyMigration(t *testing.T, db *DB, path string) {
	t.Helper()
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if _, err := db.SQL().Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

// legacyMessagesTable is `messages` in its pre-move column shape: routd's 0001
// columns plus the three its chain added. Only the frozen-migration fixture
// above needs it — every other test writes through the entity.
const legacyMessagesTable = `
DROP TABLE IF EXISTS messages;
CREATE TABLE messages (
  id TEXT PRIMARY KEY, chat_jid TEXT NOT NULL, sender TEXT NOT NULL,
  sender_name TEXT, content TEXT NOT NULL, timestamp TEXT NOT NULL,
  is_from_me INTEGER NOT NULL DEFAULT 0, is_bot_message INTEGER NOT NULL DEFAULT 0,
  forwarded_from TEXT, reply_to_id TEXT, reply_to_text TEXT, reply_to_sender TEXT,
  topic TEXT NOT NULL DEFAULT '', routed_to TEXT NOT NULL DEFAULT '',
  verb TEXT NOT NULL DEFAULT 'message', attachments TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '', is_observed INTEGER NOT NULL DEFAULT 0,
  turn_id TEXT, status TEXT NOT NULL DEFAULT 'sent', platform_id TEXT,
  chat_name TEXT NOT NULL DEFAULT '', errored INTEGER NOT NULL DEFAULT 0,
  delivery_attempts INTEGER NOT NULL DEFAULT 0, link_context TEXT);
`

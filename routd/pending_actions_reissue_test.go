package routd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// cursorSecondRow writes one ordinary message at a nanosecond instant and parks
// the chat's cursor at a position in that SAME second — the collision BUGS L97
// reports, and the normal case: an adapter truncates its platform timestamp to
// the second, so a row written in that second has only its fraction to keep it
// on the right side of `timestamp > agent_cursor`.
func cursorSecondRow(t *testing.T, db *DB, chatJID string) core.Message {
	t.Helper()
	m := core.Message{
		ID: core.MsgID("cursor-second"), ChatJID: chatJID, Sender: "google:op",
		Content: "answer the question", Timestamp: time.Now().UTC(), Verb: "message",
	}
	if err := db.PutMessage(m); err != nil {
		t.Fatalf("put the row: %v", err)
	}
	if err := db.SetAgentCursor(chatJID,
		store.MsgStamp(core.TS(m.Timestamp.Truncate(time.Second)), "platform-row")); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	return m
}

// routedChat stands up a group and the route that reaches it.
func routedChat(t *testing.T, db *DB, srv *Server, chatJID string) {
	t.Helper()
	if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Match: "platform=slack", Target: "atlas"}}})
}

// TestRowInTheCursorSecondReachesTheDispatchFeed is BUGS L97 end to end: a row
// whose instant shares a second with the cursor must still be fed to the agent.
// Under a fraction-stripping layout the dispatch scan steps over it and the turn
// never sees the message at all.
func TestRowInTheCursorSecondReachesTheDispatchFeed(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	routedChat(t, db, srv, chatJID)

	row := cursorSecondRow(t, db, chatJID)

	msgs, err := db.NewMessagesForChat(chatJID, db.GetAgentCursor(chatJID), 100)
	if err != nil {
		t.Fatalf("messages since cursor: %v", err)
	}
	if !containsID(msgs, row.ID) {
		t.Fatalf("the row is invisible to the dispatch feed: row %q vs cursor %q",
			core.TS(row.Timestamp), db.GetAgentCursor(chatJID))
	}

	// And the turn actually runs with it in its batch.
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !strings.Contains(runner.gotBatch, "answer the question") {
		t.Fatalf("the agent's batch does not carry the row: %q", runner.gotBatch)
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

// TestPollOnceDoesNotSkipAChatWithARowInTheCursorSecond covers the other gate on
// the same second: pollOnce feeds from the global minimum cursor and skips any
// chat already current on its own, comparing the batch tail against that cursor
// as text. A stripped fraction makes the chat look current and it is never
// enqueued.
func TestPollOnceDoesNotSkipAChatWithARowInTheCursorSecond(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	const chatJID = "slack:T/C/U"
	routedChat(t, db, srv, chatJID)

	row := cursorSecondRow(t, db, chatJID)

	// pollOnce enqueues; the queue is stopped in this harness, so drive the
	// dispatch the enqueue would have reached and assert the turn ran.
	srv.loop.pollOnce()
	if _, err := srv.loop.processGroupMessages(chatJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.gotTurn != row.ID {
		t.Fatalf("the poll skipped the chat holding the row: turn_id=%q want %q",
			runner.gotTurn, row.ID)
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

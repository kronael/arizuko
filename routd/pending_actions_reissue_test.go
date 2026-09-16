package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
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
// position, as the item's own sort key carries it. Reading it back through
// core.TS would render the value a second time and hide the defect under test.
func storedInstant(t *testing.T, db *DB, chatJID, id string) string {
	t.Helper()
	items, err := db.Obj().Query(t.Context(), objstore.Query{
		Entity: store.Messages, Partition: chatJID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if at, got, ok := store.MsgCoord(it.Key.Sort); ok && got == id {
			return at
		}
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

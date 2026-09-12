package routd

// poll_cursor_test.go guards what the poll loop RECORDS after it feeds a batch.
//
// The loop reads each chat from its own watermark now, so the cursor is the only
// thing standing between a message and being answered twice — or never. Both
// bodies below are set comparisons on what the NEXT poll would feed, because the
// cursor's value is supposed to change and only the set says whether it moved.
//
// Neither races. The interleaves are sequenced by the test, which is what makes
// them fail every run rather than one run in fifty: SQLite serialises writers, so
// a body that hoped to catch the collision with goroutines stays green against
// deliberately broken code.

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// seedPollChat writes n messages a second apart and answers them in order.
func seedPollChat(t *testing.T, db *DB, jid string, n int) []core.Message {
	t.Helper()
	base := time.Date(2026, 9, 12, 11, 0, 0, 0, time.UTC)
	out := make([]core.Message, 0, n)
	for i := range n {
		m := core.Message{
			ID: string(rune('a'+i)) + "-msg", ChatJID: jid, Sender: "u",
			Content: "body", Timestamp: base.Add(time.Duration(i) * time.Second),
		}
		if err := db.PutMessage(m); err != nil {
			t.Fatalf("seed: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// fedIDs answers which of a chat's messages the next poll would take.
func fedIDs(t *testing.T, db *DB, jid string) []string {
	t.Helper()
	msgs, err := db.NewMessagesForChat(jid, db.GetAgentCursor(jid), pollBatch)
	if err != nil {
		t.Fatalf("read the feed: %v", err)
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestAdvanceRecordsTheBatchItFedAndNotTheClock pins the one thing the cursor
// advance may name.
//
// MUTATION TARGET: in routd/loop.go's advance, record core.NowTS() — or the
// chat's newest message — instead of store.MessageCursor(last). Both step over
// every message that arrived after the batch was read, and this goes red saying
// the arrival was consumed without ever being fed.
func TestAdvanceRecordsTheBatchItFedAndNotTheClock(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := &Loop{db: db}
	const jid = "slack:T/C/U"
	fed := seedPollChat(t, db, jid, 3)

	// The loop feeds the batch it read, then records that batch's last message.
	l.advance(jid, fed[len(fed)-1])
	if got := fedIDs(t, db, jid); len(got) != 0 {
		t.Fatalf("the fed batch is still unfed: %v", got)
	}

	// A message that arrived while the batch was being answered.
	late := core.Message{
		ID: "late", ChatJID: jid, Sender: "u", Content: "arrived mid-turn",
		Timestamp: fed[len(fed)-1].Timestamp.Add(time.Second),
	}
	if err := db.PutMessage(late); err != nil {
		t.Fatal(err)
	}
	if got := fedIDs(t, db, jid); len(got) != 1 || got[0] != "late" {
		t.Errorf("the next poll feeds %v, want [late]: the cursor was recorded past the "+
			"batch the loop actually fed, so no agent ever answers that message", got)
	}
}

// TestAdvanceLeavesAMessageOfTheCursorsOwnInstantUnfed is the composite key's
// reason, at the loop.
//
// Two messages in one instant is not rare — 245 pairs share one inside a single
// chat on krons. A cursor that named only the instant would skip the second of
// every such pair, because the loop compares `> cursor`.
//
// MUTATION TARGET: make store.MessageCursor answer core.TS(m.Timestamp) alone.
// SetAgentCursor refuses a bare instant outright, so the advance fails and the
// first assertion goes red; drop requireMessageCursor as well and the second
// does, with the sibling message silently consumed.
func TestAdvanceLeavesAMessageOfTheCursorsOwnInstantUnfed(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := &Loop{db: db}
	const jid = "slack:T/C/U"
	at := time.Date(2026, 9, 12, 11, 0, 0, 250, time.UTC)

	first := core.Message{ID: "aaa", ChatJID: jid, Sender: "u", Content: "one", Timestamp: at}
	second := core.Message{ID: "bbb", ChatJID: jid, Sender: "u", Content: "two", Timestamp: at}
	for _, m := range []core.Message{first, second} {
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	// The loop fed only the first — a batch bound, or a message that landed
	// between the read and the advance.
	l.advance(jid, first)
	if cur := db.GetAgentCursor(jid); cur != store.MessageCursor(first) {
		t.Fatalf("the cursor = %q, want the position of the message that was fed", cur)
	}
	if got := fedIDs(t, db, jid); len(got) != 1 || got[0] != "bbb" {
		t.Errorf("the next poll feeds %v, want [bbb]: the sibling of the cursor's own "+
			"instant was consumed without ever being fed", got)
	}
}

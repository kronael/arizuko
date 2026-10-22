package store

// messages_migrate_test.go proves the thing the move can get wrong without
// failing: the cursor the poll loop advances.
//
// THE CHECK IS A SET EQUALITY, not a value comparison, because the value is
// SUPPOSED to change. A re-rendered cursor is a new string for the same place —
// so the only question worth asking is which messages are still unfed, before
// and after.
//
// The interleave below is FORCED with conformance.SlipAt rather than raced.
// SQLite serialises writers, so a body that hoped to catch the collision by
// running two goroutines stays green against deliberately broken code; this
// project has shipped that mistake more than once.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
)

// seedChat writes n messages one second apart and answers them in order.
func seedChat(t *testing.T, s *Store, jid string, n int) []core.Message {
	t.Helper()
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	out := make([]core.Message, 0, n)
	for i := range n {
		m := core.Message{
			ID: fmt.Sprintf("m%02d", i), ChatJID: jid, Sender: "u",
			Content: fmt.Sprintf("body %d", i), Timestamp: base.Add(time.Duration(i) * time.Second),
		}
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
		out = append(out, m)
	}
	return out
}

// unfedIDs answers which of a chat's messages sit after a cursor.
func unfedIDs(t *testing.T, s *Store, jid, cursor string) []string {
	t.Helper()
	msgs, err := s.NewMessagesForChat(context.Background(), jid, cursor, 0)
	if err != nil {
		t.Fatalf("read %s from %q: %v", jid, cursor, err)
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestTwoMessagesInOneInstantAreConsumedOnceEach is why the id is in the key.
//
// 245 pairs on krons share an instant inside one chat. A cursor that named only
// the instant would either re-feed both of them for ever or skip both after the
// first, because the poll loop compares `> cursor` and a bare instant sits below
// every message at it.
//
// MUTATION TARGET: drop the id from MsgStamp. The Put of the second message then
// lands on the first one's item, and the seed itself reports one message where
// two were written.
func TestTwoMessagesInOneInstantAreConsumedOnceEach(t *testing.T) {
	s := openEntities(t)
	const jid = "slack:T/C/U"
	at := time.Date(2026, 9, 12, 10, 0, 0, 500, time.UTC)
	for _, id := range []string{"aaa", "bbb"} {
		if err := s.PutMessage(core.Message{
			ID: id, ChatJID: jid, Sender: "u", Content: id, Timestamp: at,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if got := unfedIDs(t, s, jid, ""); len(got) != 2 {
		t.Fatalf("the chat holds %v; two messages of one instant collapsed onto one item", got)
	}
	// Consuming the first leaves the second, which a time-only cursor could not
	// express at all.
	first := MsgStamp(core.TS(at), "aaa")
	if got := unfedIDs(t, s, jid, first); len(got) != 1 || got[0] != "bbb" {
		t.Errorf("after consuming the first of the instant the feed holds %v, want [bbb]", got)
	}
	second := MsgStamp(core.TS(at), "bbb")
	if got := unfedIDs(t, s, jid, second); len(got) != 0 {
		t.Errorf("after consuming both the feed still holds %v", got)
	}
}

// TestAMessageArrivingUnderThePollIsFedByTheNextOne is the poll loop's own
// invariant, forced rather than raced.
//
// The loop reads a chat's batch and then advances the cursor to the LAST MESSAGE
// IT READ. A message that commits between those two steps has to survive to the
// next tick: the cursor names a position the loop actually fed, so anything
// newer is still ahead of it.
//
// MUTATION TARGET: advance to core.NowTS() — or to the chat's newest message
// rather than the batch's last — instead of MessageCursor(last). Either steps
// the cursor over the arrival and the assertion below reports it consumed
// without ever being read, which is the message the agent never answers.
func TestAMessageArrivingUnderThePollIsFedByTheNextOne(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	const jid = "slack:T/C/U"
	seeded := seedChat(t, s, jid, 3)

	// The arrival commits after the feed's own query returns, which is the one
	// window between the read and the advance.
	late := core.Message{
		ID: "late", ChatJID: jid, Sender: "u", Content: "arrived under the poll",
		Timestamp: seeded[len(seeded)-1].Timestamp.Add(time.Second),
	}
	var arriveErr error
	fired := 0
	slipped := conformance.SlipAt(s.Obj(), Messages, 1, func() {
		fired++
		arriveErr = s.PutMessage(late)
	})
	batch, err := slipped.Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Limit: pollBatchForTest,
	})
	if err != nil {
		t.Fatalf("the poll read failed: %v", err)
	}
	if arriveErr != nil {
		t.Fatalf("the arrival failed to commit: %v", arriveErr)
	}
	if fired != 1 {
		t.Fatalf("the interleave did not fire (%d times); the test proves nothing", fired)
	}
	if len(batch) != len(seeded) {
		t.Fatalf("the poll read %d messages, want the %d that existed when it started",
			len(batch), len(seeded))
	}

	// The loop advances to the last message IT READ, never to now.
	last := messageFrom(batch[len(batch)-1])
	if err := s.SetAgentCursor(jid, MessageCursor(last)); err != nil {
		t.Fatalf("advance the cursor: %v", err)
	}
	if got := unfedIDs(t, s, jid, mustCursor(t, s, jid)); len(got) != 1 || got[0] != "late" {
		t.Errorf("the next poll feeds %v, want [late]: the message that arrived under the "+
			"read was stepped over and no agent ever answers it", got)
	}
}

// pollBatchForTest is the bound the loop reads one chat's batch under. It is
// written here rather than imported because routd owns the loop and this package
// owns the read it makes.
const pollBatchForTest = 200

// mustCursor reads a chat's dispatch cursor or fails the test.
func mustCursor(t *testing.T, s *Store, jid string) string {
	t.Helper()
	cur, err := s.AgentCursor(jid)
	if err != nil {
		t.Fatalf("read %s's cursor: %v", jid, err)
	}
	return cur
}

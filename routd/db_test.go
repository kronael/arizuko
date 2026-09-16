package routd

import (
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// The chats row carries four independent pins — is_group, agent_cursor,
// sticky_group, sticky_topic — and four separate writers upsert into it. Each
// writer names ONLY its own column in the DO UPDATE clause. An `INSERT OR
// REPLACE`, or a DO UPDATE that lists a column it does not own, silently drops
// the other three: the chat keeps routing but loses its @group pin.
func TestChatUpsertsPreserveTheOtherColumns(t *testing.T) {
	db := memDB(t)
	const jid = "telegram:42"

	if err := db.SetStickyGroup(jid, "support"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStickyTopic(jid, "billing"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentCursor(jid, store.MsgStamp("2026-01-01T00:00:00.000000000Z", "m1")); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}

	group, topic := db.StickyState(jid)
	if group != "support" {
		t.Errorf("sticky_group = %q, want support — a later upsert cleared it", group)
	}
	if topic != "billing" {
		t.Errorf("sticky_topic = %q, want billing — a later upsert cleared it", topic)
	}
	if got := db.GetAgentCursor(jid); got != store.MsgStamp("2026-01-01T00:00:00.000000000Z", "m1") {
		t.Errorf("agent_cursor = %q, want the value set — a later upsert cleared it", got)
	}
	if !db.ChatIsGroup(jid) {
		t.Error("is_group not persisted")
	}
}

// The observe ingest marks a whole batch at once. One UPDATE with an IN list
// binds one placeholder per id, so an off-by-one in that list either errors or
// silently marks the wrong rows — and a half-marked batch re-enters the next
// poll as unobserved, which fires the turn the observe rule exists to suppress.
func TestMarkMessagesObservedMarksEveryIDInTheBatch(t *testing.T) {
	db := memDB(t)
	st := store.NewOver(db.Obj())
	const jid = "slack:T/C/X"
	now := time.Now().UTC()
	var batch []core.Message
	for i, id := range []string{"a", "b", "c"} {
		m := core.Message{ID: id, ChatJID: jid, Sender: "u", Content: id,
			Timestamp: now.Add(time.Duration(i) * time.Second)}
		batch = append(batch, m)
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// One row outside the batch: it must stay unobserved.
	if err := db.PutMessage(core.Message{ID: "d", ChatJID: jid, Sender: "u",
		Content: "d", Timestamp: now.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	if err := db.MarkMessagesObserved("demo", batch); err != nil {
		t.Fatalf("MarkMessagesObserved: %v", err)
	}
	for _, m := range batch {
		got, ok := st.MessageByID(jid, m.ID)
		if !ok {
			t.Fatalf("id %q is gone", m.ID)
		}
		if got.RoutedTo != "demo" {
			t.Errorf("id %q: routed_to=%q, want demo", m.ID, got.RoutedTo)
		}
	}
	// The ambient window IS the observed flag's only reader, so it is what the
	// batch has to have moved — all three of it, and nothing else.
	window := db.ObservedSince("demo", "", 10, 100000)
	var seen []string
	for _, m := range window {
		seen = append(seen, m.ID)
	}
	if !reflect.DeepEqual(seen, []string{"a", "b", "c"}) {
		t.Errorf("ambient window = %v, want the whole batch and only it", seen)
	}
	outside, ok := st.MessageByID(jid, "d")
	if !ok {
		t.Fatal("the row outside the batch is gone")
	}
	if outside.RoutedTo != "" {
		t.Errorf("a row outside the batch was routed to %q", outside.RoutedTo)
	}
}

package routd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Open never creates routd.db. Without this, a routd pointed at the wrong
// store/<owner>/ — a typo'd mount, an unfinished file move — migrates a fresh
// file and serves a fully-formed instance with zero groups, zero acl and zero
// secrets, reporting healthy the whole time (spec 5/16 step 7).
func TestOpenRefusesToManufactureAnEmptyInstance(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Fatal("Open created routd.db on an empty dir; that is a green boot on an empty instance")
	}
	if _, err := os.Stat(filepath.Join(dir, "routd", "routd.db")); !os.IsNotExist(err) {
		t.Errorf("Open left a routd.db behind (err=%v)", err)
	}

	// Create is the one entry point that may make it, and Open takes over after.
	db, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after Create: %v", err)
	}
	db2.Close()
}

// TestOpenNoSiblingDB asserts routd.Create makes ONLY routd.db in the store dir
// and opens NO sibling messages.db — the last sibling-read (pane_sessions) moved
// to routd's own DB (migration 0010). routd is now self-contained: every table
// it reads is in routd.db; cross-daemon data arrives over HTTP (authd/runed).
func TestOpenNoSiblingDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	// routd.db exists; messages.db must NOT (routd never opens or creates it).
	if _, err := os.Stat(filepath.Join(dir, "routd", "routd.db")); err != nil {
		t.Fatalf("routd.db not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "messages.db")); !os.IsNotExist(err) {
		t.Errorf("messages.db must not exist — routd opens NO sibling DB (err=%v)", err)
	}

	// pane_sessions lives in routd.db now (migration 0010), readable by the
	// owner's own handle.
	var name string
	if err := db.SQL().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='pane_sessions'`).Scan(&name); err != nil {
		t.Fatalf("pane_sessions table missing from routd.db: %v", err)
	}
}

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
	st := store.New(db.SQL())
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

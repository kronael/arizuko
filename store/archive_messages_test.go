package store_test

// Message-history archive tests — spec 5/8 "Message history" + Finding 4.
//
// The fixture is routd's OWN tables, opened from the EXTERNAL test package:
// routd imports store, so an internal store test file could not import it back.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// routdMessages hands back routd's own tables, wrapped as the owner the archive
// lane is a method on.
func routdMessages(t *testing.T) *store.Store {
	t.Helper()
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewOver(db.Obj())
}

// insertMsg seeds one message through the LIVE writer: the delivery status
// decides whether the row owes a delivery at all, and `is_observed` is the
// folder feed's stamp rather than a column. A fixture that wrote the item by
// hand could build one the daemon cannot.
func insertMsg(t *testing.T, st *store.Store, m store.ArchiveMessageRow) {
	t.Helper()
	ctx := context.Background()
	at, err := store.ReadMessageInstant(m.Timestamp)
	if err != nil {
		t.Fatalf("seed message %s: %v", m.ID, err)
	}
	msg := core.Message{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName,
		Content: m.Content, Timestamp: at, FromMe: m.IsFromMe, BotMsg: m.IsBotMessage,
		ForwardedFrom: m.ForwardedFrom, ReplyToID: m.ReplyToID, ReplyToText: m.ReplyToText,
		ReplyToSender: m.ReplyToSender, Topic: m.Topic, RoutedTo: m.RoutedTo, Verb: m.Verb,
		Attachments: m.Attachments, Source: m.Source, TurnID: m.TurnID,
		PlatformID: m.PlatformID, ChatName: m.ChatName, Errored: m.Errored,
		LinkContext: m.LinkContext,
	}
	status := m.Status
	if status == "" {
		status = core.MessageStatusSent
	}
	if err := st.PutOutbound(ctx, msg, status); err != nil {
		t.Fatalf("seed message %s: %v", m.ID, err)
	}
	if m.IsObserved {
		if err := st.MarkMessagesObserved(ctx, m.RoutedTo, []core.Message{msg}); err != nil {
			t.Fatalf("observe message %s: %v", m.ID, err)
		}
	}
}

// countMessages counts ONE chat's rows. `messages` is an entity, so the count
// is a partition read rather than a column query.
func countMessages(t *testing.T, st *store.Store, chatJID string) int {
	t.Helper()
	msgs, err := st.MessagesSince(chatJID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// A restore that loses a column loses it silently: the rows are all there and
// the count matches. Every field, including each of the four booleans, has to
// survive the JSONL round trip.
func TestMessagesRoundTripEveryColumn(t *testing.T) {
	srcSt := routdMessages(t)
	full := store.ArchiveMessageRow{
		ID: "m1", ChatJID: "slack:C1", Sender: "u7", SenderName: "Alice",
		Content: "hello", Timestamp: ts(t, "10:00"),
		IsFromMe: true, IsBotMessage: true, ForwardedFrom: "slack:C0",
		ReplyToID: "m0", ReplyToText: "earlier", ReplyToSender: "u8",
		Topic: "design", RoutedTo: "acme/eng", Verb: "reply",
		Attachments: `[{"n":1}]`, Source: "platform", IsObserved: true,
		TurnID: "t-1", Status: core.MessageStatusFailed, PlatformID: "p-1",
		ChatName: "#eng", Errored: true, LinkContext: "ctx",
	}
	// A second row with every boolean the other way, so a swapped or dropped
	// flag cannot pass by matching the first row's value.
	sparse := store.ArchiveMessageRow{
		ID: "m2", ChatJID: "slack:C1", Sender: "u9",
		Content: "bare", Timestamp: ts(t, "10:01"),
	}
	// The attempt count lives on the delivery item rather than the message, and
	// only exhausting the retries puts one there: the row is seeded pending and
	// driven to the dead-letter set the way a real send fails. A dead letter that
	// restores claiming never to have been tried is the shape this pins.
	owed := full
	owed.Status = core.MessageStatusPending
	insertMsg(t, srcSt, owed)
	insertMsg(t, srcSt, sparse)

	dying := core.Message{ID: full.ID, ChatJID: full.ChatJID,
		Timestamp: mustInstant(t, full.Timestamp)}
	for range 2 {
		if _, _, err := srcSt.RecordDeliveryFailure(context.Background(), dying, 2); err != nil {
			t.Fatalf("record a delivery failure: %v", err)
		}
	}

	var buf bytes.Buffer
	n, err := srcSt.ExportMessagesJSONL(context.Background(), &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 2 {
		t.Fatalf("exported %d rows, want 2", n)
	}
	if lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; lines != 2 {
		t.Errorf("JSONL has %d lines for 2 rows", lines)
	}

	dstSt := routdMessages(t)
	imported, touched, refused, err := dstSt.ImportMessagesJSONL(context.Background(), bytes.NewReader(buf.Bytes()), 0)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported %d rows, want 2", imported)
	}
	if !touched["slack:C1"] || len(touched) != 1 {
		t.Errorf("touched = %v, want just slack:C1", touched)
	}
	if len(refused) != 0 {
		t.Errorf("refused %v from an archive whose every timestamp is renderable", refused)
	}

	var back bytes.Buffer
	if _, err := dstSt.ExportMessagesJSONL(context.Background(), &back); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if back.String() != buf.String() {
		t.Errorf("the round trip changed the rows\n src: %s\ndest: %s", buf.String(), back.String())
	}

	dead, err := dstSt.DeadLetters(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("read the restored dead letters: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("%d dead letters restored, want 1", len(dead))
	}
	if dead[0].Attempts != 2 {
		t.Errorf("the restored dead letter reports %d attempts, want the 2 it cost",
			dead[0].Attempts)
	}
}

// mustInstant parses an archived timestamp or fails.
func mustInstant(t *testing.T, raw string) time.Time {
	t.Helper()
	at, err := store.ReadMessageInstant(raw)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// "Idempotent bulk append, not rebuild": re-running the same archive, or
// restoring onto a target that already holds some of the same history, must
// be a no-op on rows already present — never a duplicate, never an error.
func TestMessagesImportIsIdempotentAndAppends(t *testing.T) {
	srcSt := routdMessages(t)
	insertMsg(t, srcSt, store.ArchiveMessageRow{ID: "m1", ChatJID: "slack:C1", Sender: "u1", Content: "a", Timestamp: ts(t, "10:00")})
	insertMsg(t, srcSt, store.ArchiveMessageRow{ID: "m2", ChatJID: "slack:C1", Sender: "u1", Content: "b", Timestamp: ts(t, "10:01")})

	var buf bytes.Buffer
	if _, err := srcSt.ExportMessagesJSONL(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}

	dstSt := routdMessages(t)
	// The target already holds m1, with DIFFERENT content: the existing row
	// must win, because it is the live one.
	insertMsg(t, dstSt, store.ArchiveMessageRow{ID: "m1", ChatJID: "slack:C1", Sender: "u1", Content: "LIVE", Timestamp: ts(t, "10:00")})

	for pass := 1; pass <= 2; pass++ {
		if _, _, _, err := dstSt.ImportMessagesJSONL(context.Background(), bytes.NewReader(buf.Bytes()), 0); err != nil {
			t.Fatalf("import pass %d: %v", pass, err)
		}
		if got := countMessages(t, dstSt, "slack:C1"); got != 2 {
			t.Fatalf("after pass %d the target holds %d rows, want 2", pass, got)
		}
	}

	live, ok := dstSt.MessageByID("slack:C1", "m1")
	if !ok {
		t.Fatal("the live row is gone")
	}
	if live.Content != "LIVE" {
		t.Errorf("the import overwrote a live row: content = %q, want LIVE", live.Content)
	}
}

// The batch bound exists so a multi-hour transfer does not run as a single
// write. Whatever the batch size, the result must be the same set of rows —
// including a final partial batch.
func TestMessagesImportBatchSizeDoesNotChangeTheResult(t *testing.T) {
	srcSt := routdMessages(t)
	for i, hhmm := range []string{"10:00", "10:01", "10:02", "10:03", "10:04", "10:05", "10:06"} {
		insertMsg(t, srcSt, store.ArchiveMessageRow{
			ID:      string(rune('a' + i)),
			ChatJID: "slack:C1", Sender: "u1", Content: hhmm,
			Timestamp: ts(t, hhmm),
		})
	}
	var buf bytes.Buffer
	if _, err := srcSt.ExportMessagesJSONL(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}

	// 3 does not divide 7, so the last batch is partial — the case a flush
	// that only fires on a full batch would drop.
	for _, batch := range []int{1, 3, 500, 0, -1} {
		dstSt := routdMessages(t)
		n, _, _, err := dstSt.ImportMessagesJSONL(context.Background(), bytes.NewReader(buf.Bytes()), batch)
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		if n != 7 || countMessages(t, dstSt, "slack:C1") != 7 {
			t.Errorf("batch %d: reported %d, stored %d, want 7 and 7", batch, n, countMessages(t, dstSt, "slack:C1"))
		}
	}
}

// A truncated or corrupted archive must fail loudly. Silently stopping at the
// bad line would restore a partial history and report success.
func TestMessagesImportRejectsMalformedJSONL(t *testing.T) {
	dstSt := routdMessages(t)
	bad := `{"id":"m1","chat_jid":"slack:C1","sender":"u","content":"a","timestamp":"` + ts(t, "10:00") + `"}
{"id":"m2","chat_jid":
`
	if _, _, _, err := dstSt.ImportMessagesJSONL(context.Background(), strings.NewReader(bad), 0); err == nil {
		t.Fatal("a truncated archive imported without an error")
	}
}

// DeriveAgentCursors

// cursorOf reads a chat's dispatch cursor through the entity, which is where it
// lives: the fixture asserts what the poll loop would read, not what a column
// holds (spec 6/37 step 7).
func cursorOf(t *testing.T, st *store.Store, jid string) string {
	t.Helper()
	c, err := st.AgentCursor(jid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Without a cursor the poller re-opens every restored message as new: a turn
// per historical message, the agent answering the entire archive. The cursor
// is MAX(timestamp) over each restored chat.
func TestDeriveAgentCursorsParksThePollerAtTheEndOfTheHistory(t *testing.T) {
	dbSt := routdMessages(t)
	insertMsg(t, dbSt, store.ArchiveMessageRow{ID: "a1", ChatJID: "slack:C1", Sender: "u", Content: "x", Timestamp: ts(t, "10:00")})
	insertMsg(t, dbSt, store.ArchiveMessageRow{ID: "a2", ChatJID: "slack:C1", Sender: "u", Content: "y", Timestamp: ts(t, "12:00")})
	insertMsg(t, dbSt, store.ArchiveMessageRow{ID: "b1", ChatJID: "slack:C2", Sender: "u", Content: "z", Timestamp: ts(t, "09:00")})

	n, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true, "slack:C2": true})
	if err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	if n != 2 {
		t.Errorf("moved %d cursors, want 2", n)
	}
	// The cursor holds the newest message's WHOLE key: a bare instant sorts
	// below every message at it and would feed that instant again on every poll.
	if got, want := cursorOf(t, dbSt, "slack:C1"), store.MsgStamp(ts(t, "12:00"), "a2"); got != want {
		t.Errorf("slack:C1 cursor = %q, want the newest message's key %q", got, want)
	}
	if got, want := cursorOf(t, dbSt, "slack:C2"), store.MsgStamp(ts(t, "09:00"), "b1"); got != want {
		t.Errorf("slack:C2 cursor = %q, want %q", got, want)
	}

	// Nothing touched, nothing to do.
	if n, err := dbSt.DeriveAgentCursors(context.Background(), nil); err != nil || n != 0 {
		t.Errorf("empty touched set gave (%d, %v), want (0, nil)", n, err)
	}
}

// The guard that makes a restore onto a LIVE target safe. Moving an existing
// cursor backward would re-open every message the poller already consumed —
// the agent replying to the whole recent history of a running instance.
func TestDeriveAgentCursorsNeverWalksALiveCursorBackward(t *testing.T) {
	dbSt := routdMessages(t)
	insertMsg(t, dbSt, store.ArchiveMessageRow{ID: "old", ChatJID: "slack:C1", Sender: "u", Content: "x", Timestamp: ts(t, "08:00")})
	live := store.MsgStamp(ts(t, "20:00"), "live")
	if err := dbSt.SetAgentCursor("slack:C1", live); err != nil {
		t.Fatal(err)
	}

	n, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true})
	if err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	if got := cursorOf(t, dbSt, "slack:C1"); got != live {
		t.Fatalf("the live cursor was walked back to %q; the agent would answer its whole recent history", got)
	}
	if n != 0 {
		t.Errorf("reported %d cursors moved, want 0 — nothing changed", n)
	}

	// A restore that DOES extend the history past the live cursor moves it.
	insertMsg(t, dbSt, store.ArchiveMessageRow{ID: "new", ChatJID: "slack:C1", Sender: "u", Content: "y", Timestamp: ts(t, "22:00")})
	if _, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true}); err != nil {
		t.Fatal(err)
	}
	if got, want := cursorOf(t, dbSt, "slack:C1"), store.MsgStamp(ts(t, "22:00"), "new"); got != want {
		t.Errorf("cursor = %q, want it advanced to the new tail %q", got, want)
	}
}

// TestDeriveAgentCursorsDerivesEveryChatItCanAndSkipsTheRest is the resilience
// the sweep needs, and the reason is one row shape.
//
// An archive can carry a hand-written `not an instant at all`, and the import
// REFUSES such a row rather than writing an item with no position — so the chat
// it named is restored empty. The chats are swept in sorted order, and "slack:A"
// sorts before "slack:Z": a sweep that stopped at the empty chat would leave
// every alphabetically later chat with NO cursor at all, and the poll loop reads
// a restored history with no cursor as entirely unread — the state this
// operation exists to prevent (spec 5/8 Finding 4).
func TestDeriveAgentCursorsDerivesEveryChatItCanAndSkipsTheRest(t *testing.T) {
	dbSt := routdMessages(t)
	doc := archiveOf(t, []store.ArchiveMessageRow{
		{ID: "a1", ChatJID: "slack:A", Sender: "u", Content: "poked in by hand",
			Timestamp: "not an instant at all"},
		{ID: "z1", ChatJID: "slack:Z", Sender: "u", Content: "ordinary",
			Timestamp: ts(t, "12:00")},
	})
	_, touched, refused, err := dbSt.ImportMessagesJSONL(context.Background(),
		bytes.NewReader(doc), 0)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(refused) != 1 || refused[0].ID != "a1" {
		t.Fatalf("refused = %v, want exactly the row with no position", refused)
	}

	moved, err := dbSt.DeriveAgentCursors(context.Background(), touched)
	if err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved %d cursors, want 1 — the empty chat has none to move", moved)
	}
	if got, want := cursorOf(t, dbSt, "slack:Z"), store.MsgStamp(ts(t, "12:00"), "z1"); got != want {
		t.Fatalf("the later chat's cursor = %q, want %q — one empty chat stopped the "+
			"sweep, and every chat after it reads its whole restored history as unread", got,
			want)
	}
	if got := cursorOf(t, dbSt, "slack:A"); got != "" {
		t.Errorf("the chat whose only row was refused carries the cursor %q", got)
	}
}

// TestDeriveAgentCursorsParksAtTheRenderedInstant pins the other half: the
// archive is where a foreign string is brought into the one layout, and the
// cursor is then COPIED from the key the import built.
//
// A rendered instant sorts at or above the raw value it came from, so the poller
// still parks at the end of the restored history, which is what this is for.
func TestDeriveAgentCursorsParksAtTheRenderedInstant(t *testing.T) {
	dbSt := routdMessages(t)
	// The shape routd/migrations/0044 deliberately leaves alone rather than
	// invent a zone for. Two rows carry it on krons today.
	const handWritten = "2026-04-02 12:38:47"
	doc := archiveOf(t, []store.ArchiveMessageRow{
		{ID: "h1", ChatJID: "slack:H", Sender: "u", Content: "poked in by hand",
			Timestamp: handWritten},
	})
	if _, touched, _, err := dbSt.ImportMessagesJSONL(context.Background(),
		bytes.NewReader(doc), 0); err != nil {
		t.Fatalf("import: %v", err)
	} else if _, err := dbSt.DeriveAgentCursors(context.Background(), touched); err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	got := cursorOf(t, dbSt, "slack:H")
	if want := store.MsgStamp("2026-04-02T12:38:47.000000000Z", "h1"); got != want {
		t.Fatalf("cursor = %q, want the rendered instant's key %q", got, want)
	}
	if !(got > handWritten) {
		t.Errorf("the cursor %q sorts below the row %q it came from, so the poller re-feeds it",
			got, handWritten)
	}
}

package store_test

// Message-history archive tests — spec 5/8 "Message history" + Finding 4.
//
// archive_test.go's header says these live in cmd/arizuko/archive_test.go
// against the real routd schema. They did not: nothing in the repo named
// ExportMessagesJSONL, ImportMessagesJSONL or DeriveAgentCursors, and all
// three read 0%. The reason the header gives is real — store's frozen
// messages.db predates routd/migrations/0019 (link_context), so
// archiveMessageCols cannot run against it — so these use routd.Create, the
// daemon's own migration sequence, from the EXTERNAL test package (routd
// imports resreg, which an internal resreg test file could not do).

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// routdMessages builds routd.db through routd's own migration chain and wraps
// it as the owner the archive lane is a method on. The raw pool comes back too
// because the fixtures insert message columns no accessor writes.
func routdMessages(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	db, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("routd.Create: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.New(db.SQL()), db.SQL()
}

func insertMsg(t *testing.T, db *sql.DB, m store.ArchiveMessageRow) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO messages
		(id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
		 is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
		 topic, routed_to, verb, attachments, source, is_observed, turn_id, status,
		 platform_id, chat_name, errored, link_context)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ChatJID, m.Sender, m.SenderName, m.Content, m.Timestamp,
		b2i(m.IsFromMe), b2i(m.IsBotMessage), m.ForwardedFrom, m.ReplyToID,
		m.ReplyToText, m.ReplyToSender, m.Topic, m.RoutedTo, m.Verb, m.Attachments,
		m.Source, b2i(m.IsObserved), m.TurnID, m.Status, m.PlatformID, m.ChatName,
		b2i(m.Errored), m.LinkContext)
	if err != nil {
		t.Fatalf("seed message %s: %v", m.ID, err)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func countMessages(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A restore that loses a column loses it silently: the rows are all there and
// the count matches. Every field, including each of the four booleans, has to
// survive the JSONL round trip.
func TestMessagesRoundTripEveryColumn(t *testing.T) {
	srcSt, src := routdMessages(t)
	full := store.ArchiveMessageRow{
		ID: "m1", ChatJID: "slack:C1", Sender: "u7", SenderName: "Alice",
		Content: "hello", Timestamp: ts(t, "10:00"),
		IsFromMe: true, IsBotMessage: true, ForwardedFrom: "slack:C0",
		ReplyToID: "m0", ReplyToText: "earlier", ReplyToSender: "u8",
		Topic: "design", RoutedTo: "acme/eng", Verb: "reply",
		Attachments: `[{"n":1}]`, Source: "platform", IsObserved: true,
		TurnID: "t-1", Status: "delivered", PlatformID: "p-1",
		ChatName: "#eng", Errored: true, LinkContext: "ctx",
	}
	// A second row with every boolean the other way, so a swapped or dropped
	// flag cannot pass by matching the first row's value.
	sparse := store.ArchiveMessageRow{
		ID: "m2", ChatJID: "slack:C1", Sender: "u9",
		Content: "bare", Timestamp: ts(t, "10:01"),
	}
	insertMsg(t, src, full)
	insertMsg(t, src, sparse)

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

	dstSt, _ := routdMessages(t)
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
}

// "Idempotent bulk append, not rebuild": re-running the same archive, or
// restoring onto a target that already holds some of the same history, must
// be a no-op on rows already present — never a duplicate, never an error.
func TestMessagesImportIsIdempotentAndAppends(t *testing.T) {
	srcSt, src := routdMessages(t)
	insertMsg(t, src, store.ArchiveMessageRow{ID: "m1", ChatJID: "slack:C1", Sender: "u1", Content: "a", Timestamp: ts(t, "10:00")})
	insertMsg(t, src, store.ArchiveMessageRow{ID: "m2", ChatJID: "slack:C1", Sender: "u1", Content: "b", Timestamp: ts(t, "10:01")})

	var buf bytes.Buffer
	if _, err := srcSt.ExportMessagesJSONL(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}

	dstSt, dst := routdMessages(t)
	// The target already holds m1, with DIFFERENT content: the existing row
	// must win, because it is the live one.
	insertMsg(t, dst, store.ArchiveMessageRow{ID: "m1", ChatJID: "slack:C1", Sender: "u1", Content: "LIVE", Timestamp: ts(t, "10:00")})

	for pass := 1; pass <= 2; pass++ {
		if _, _, _, err := dstSt.ImportMessagesJSONL(context.Background(), bytes.NewReader(buf.Bytes()), 0); err != nil {
			t.Fatalf("import pass %d: %v", pass, err)
		}
		if got := countMessages(t, dst); got != 2 {
			t.Fatalf("after pass %d the target holds %d rows, want 2", pass, got)
		}
	}

	var content string
	if err := dst.QueryRow("SELECT content FROM messages WHERE id='m1'").Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "LIVE" {
		t.Errorf("the import overwrote a live row: content = %q, want LIVE", content)
	}
}

// The batch bound exists so a multi-hour transfer does not hold routd.db's
// write lock for its whole duration. Whatever the batch size, the result must
// be the same set of rows — including a final partial batch.
func TestMessagesImportBatchSizeDoesNotChangeTheResult(t *testing.T) {
	srcSt, src := routdMessages(t)
	for i, hhmm := range []string{"10:00", "10:01", "10:02", "10:03", "10:04", "10:05", "10:06"} {
		insertMsg(t, src, store.ArchiveMessageRow{
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
		dstSt, dst := routdMessages(t)
		n, _, _, err := dstSt.ImportMessagesJSONL(context.Background(), bytes.NewReader(buf.Bytes()), batch)
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		if n != 7 || countMessages(t, dst) != 7 {
			t.Errorf("batch %d: reported %d, stored %d, want 7 and 7", batch, n, countMessages(t, dst))
		}
	}
}

// A truncated or corrupted archive must fail loudly. Silently stopping at the
// bad line would restore a partial history and report success.
func TestMessagesImportRejectsMalformedJSONL(t *testing.T) {
	dstSt, _ := routdMessages(t)
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
	dbSt, db := routdMessages(t)
	insertMsg(t, db, store.ArchiveMessageRow{ID: "a1", ChatJID: "slack:C1", Sender: "u", Content: "x", Timestamp: ts(t, "10:00")})
	insertMsg(t, db, store.ArchiveMessageRow{ID: "a2", ChatJID: "slack:C1", Sender: "u", Content: "y", Timestamp: ts(t, "12:00")})
	insertMsg(t, db, store.ArchiveMessageRow{ID: "b1", ChatJID: "slack:C2", Sender: "u", Content: "z", Timestamp: ts(t, "09:00")})

	n, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true, "slack:C2": true})
	if err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	if n != 2 {
		t.Errorf("moved %d cursors, want 2", n)
	}
	if got := cursorOf(t, dbSt, "slack:C1"); got != ts(t, "12:00") {
		t.Errorf("slack:C1 cursor = %q, want the chat's MAX(timestamp)", got)
	}
	if got := cursorOf(t, dbSt, "slack:C2"); got != ts(t, "09:00") {
		t.Errorf("slack:C2 cursor = %q", got)
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
	dbSt, db := routdMessages(t)
	insertMsg(t, db, store.ArchiveMessageRow{ID: "old", ChatJID: "slack:C1", Sender: "u", Content: "x", Timestamp: ts(t, "08:00")})
	if err := dbSt.SetAgentCursor("slack:C1", ts(t, "20:00")); err != nil {
		t.Fatal(err)
	}

	n, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true})
	if err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	if got := cursorOf(t, dbSt, "slack:C1"); got != ts(t, "20:00") {
		t.Fatalf("the live cursor was walked back to %q; the agent would answer its whole recent history", got)
	}
	if n != 0 {
		t.Errorf("reported %d cursors moved, want 0 — nothing changed", n)
	}

	// A restore that DOES extend the history past the live cursor moves it.
	insertMsg(t, db, store.ArchiveMessageRow{ID: "new", ChatJID: "slack:C1", Sender: "u", Content: "y", Timestamp: ts(t, "22:00")})
	if _, err := dbSt.DeriveAgentCursors(context.Background(), map[string]bool{"slack:C1": true}); err != nil {
		t.Fatal(err)
	}
	if got := cursorOf(t, dbSt, "slack:C1"); got != ts(t, "22:00") {
		t.Errorf("cursor = %q, want it advanced to the new tail", got)
	}
}

// TestDeriveAgentCursorsDerivesEveryChatItCanAndNamesTheRest is the resilience
// the sweep needs, and the reason is one row shape.
//
// Migration 0044 deliberately leaves a hand-written `2026-04-02 12:38:47` alone,
// because padding a ROW would invent a zone it never had — two krons rows carry
// exactly that. MAX(timestamp) over such a chat is nineteen bytes, and the
// dispatch cursor is declared at core.TS's width, so the write refuses it. The
// chats are swept in sorted order, so a sweep that stopped there left every
// alphabetically later chat with NO cursor at all, and the poll loop reads a
// restored history with no cursor as entirely unread — the state this operation
// exists to prevent (spec 5/8 Finding 4).
func TestDeriveAgentCursorsDerivesEveryChatItCanAndNamesTheRest(t *testing.T) {
	dbSt, db := routdMessages(t)
	// "slack:A" sorts before "slack:Z", so the unreadable chat is swept first.
	insertMsg(t, db, store.ArchiveMessageRow{ID: "a1", ChatJID: "slack:A", Sender: "u",
		Content: "poked in by hand", Timestamp: "not an instant at all"})
	insertMsg(t, db, store.ArchiveMessageRow{ID: "z1", ChatJID: "slack:Z", Sender: "u",
		Content: "ordinary", Timestamp: ts(t, "12:00")})

	moved, err := dbSt.DeriveAgentCursors(context.Background(),
		map[string]bool{"slack:A": true, "slack:Z": true})
	if err == nil {
		t.Fatal("a chat this cannot derive was not reported; a restore that derived only " +
			"some cursors must say which ones it missed")
	}
	if !strings.Contains(err.Error(), "slack:A") {
		t.Errorf("the report does not name the chat it could not derive: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved %d cursors, want 1", moved)
	}
	if got := cursorOf(t, dbSt, "slack:Z"); got != ts(t, "12:00") {
		t.Fatalf("the later chat's cursor = %q, want %q — one unreadable chat stopped the "+
			"sweep, and every chat after it reads its whole restored history as unread", got,
			ts(t, "12:00"))
	}
}

// TestDeriveAgentCursorsRendersTheInstantItFinds pins the other half: the raw
// column value is whatever the writer left, and the cursor attribute is declared
// at one width.
//
// Rendering a CURSOR is safe where rendering the row is not — a rendered instant
// sorts at or above the row it came from, so the poller still parks at the end
// of the restored history, which is what this is for.
func TestDeriveAgentCursorsRendersTheInstantItFinds(t *testing.T) {
	dbSt, db := routdMessages(t)
	const handWritten = "2026-04-02 12:38:47"
	insertMsg(t, db, store.ArchiveMessageRow{ID: "h1", ChatJID: "slack:H", Sender: "u",
		Content: "poked in by hand", Timestamp: handWritten})

	if _, err := dbSt.DeriveAgentCursors(context.Background(),
		map[string]bool{"slack:H": true}); err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}
	got := cursorOf(t, dbSt, "slack:H")
	if got != "2026-04-02T12:38:47.000000000Z" {
		t.Fatalf("cursor = %q, want the rendered instant", got)
	}
	if !(got > handWritten) {
		t.Errorf("the cursor %q sorts below the row %q it came from, so the poller re-feeds it",
			got, handWritten)
	}
}

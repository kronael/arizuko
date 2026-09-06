package resreg_test

// The archive is the one place a foreign string reaches messages.timestamp, a
// column whose whole guarantee is that its string order is its time order
// (core.TSLayout). These cover what an import does with a width the column no
// longer writes, and with a value that is not an instant at all — BUGS L219,
// the hole left in L97's fix.

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
)

// ts renders a time on the fixtures' shared day through core.TS. A fixture is a
// writer: one that hand-formats this column brings a second format to it, which
// is the defect under test rather than a way to set it up (L97).
func ts(t *testing.T, hhmm string) string {
	t.Helper()
	return core.TS(instant(t, hhmm))
}

func instant(t *testing.T, hhmm string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, "2026-08-27T"+hhmm+":00Z")
	if err != nil {
		t.Fatalf("fixture time %q: %v", hhmm, err)
	}
	return v
}

// archiveOf renders rows the way a real archive is rendered: seed a routd.db
// and let ExportMessagesJSONL write the document. Only the timestamps are
// handed in verbatim, because their width is the subject.
func archiveOf(t *testing.T, rows []resreg.ArchiveMessageRow) []byte {
	t.Helper()
	src := routdDB(t)
	for _, m := range rows {
		insertMsg(t, src, m)
	}
	var buf bytes.Buffer
	if _, err := resreg.ExportMessagesJSONL(context.Background(), src, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.Bytes()
}

func storedTS(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var got string
	if err := db.QueryRow("SELECT timestamp FROM messages WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return got
}

// The defect. An archive written before the column was fixed width carries a
// whole second as `…:41Z` and a fraction of that same second as `…:41.882…Z`.
// `.` is 0x2E and `Z` is 0x5A, so the LATER instant sorts FIRST: MAX(timestamp)
// picks the earlier row, the derived cursor parks there, and the newest message
// in the chat is behind a cursor that only moves forward — invisible for good.
func TestImportedHistoryLeavesNoRowBehindTheCursorItDerives(t *testing.T) {
	newest := instant(t, "19:06").Add(41*time.Second + 882458214*time.Nanosecond)
	oldest := instant(t, "19:06").Add(41 * time.Second)

	doc := archiveOf(t, []resreg.ArchiveMessageRow{
		{ID: "whole", ChatJID: "slack:C1", Sender: "u", Content: "older",
			Timestamp: oldest.Format(time.RFC3339Nano)},
		{ID: "frac", ChatJID: "slack:C1", Sender: "u", Content: "the newest message",
			Timestamp: newest.Format(time.RFC3339Nano)},
	})

	dst := routdDB(t)
	if _, touched, _, err := resreg.ImportMessagesJSONL(context.Background(), dst, bytes.NewReader(doc), 0); err != nil {
		t.Fatalf("import: %v", err)
	} else if _, err := resreg.DeriveAgentCursors(context.Background(), dst, touched); err != nil {
		t.Fatalf("DeriveAgentCursors: %v", err)
	}

	cursor := cursorOf(t, dst, "slack:C1")
	if cursor != core.TS(newest) {
		t.Errorf("cursor = %q, want the newest instant %q", cursor, core.TS(newest))
	}
	// The failure the cursor causes, asserted as the poller sees it: no stored
	// row may sort at or above the cursor, or it never reaches the agent.
	var hidden int
	if err := dst.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE chat_jid='slack:C1' AND timestamp > ?`,
		cursor).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	if hidden != 0 {
		t.Errorf("%d restored rows sort after the cursor derived from them; the agent never sees them", hidden)
	}
	if got := storedTS(t, dst, "frac"); got != core.TS(newest) {
		t.Errorf("stored timestamp = %q, want the core.TS rendering %q", got, core.TS(newest))
	}
}

// Every width an archive can carry is the same instant, and each has to land in
// the one layout — a mixed-width column is what costs the ordering.
func TestImportRendersEveryArchivedWidthThroughTheOneRenderer(t *testing.T) {
	base := instant(t, "12:00").Add(37 * time.Second)
	cases := []struct {
		id  string
		raw string
		at  time.Time
	}{
		{"whole", base.Format(time.RFC3339Nano), base},
		{"stripped", base.Add(500 * time.Millisecond).Format(time.RFC3339Nano), base.Add(500 * time.Millisecond)},
		{"padded", core.TS(base.Add(882458214 * time.Nanosecond)), base.Add(882458214 * time.Nanosecond)},
		{"offset", base.In(time.FixedZone("CET", 3600)).Format(time.RFC3339Nano), base},
	}
	var rows []resreg.ArchiveMessageRow
	for _, c := range cases {
		rows = append(rows, resreg.ArchiveMessageRow{
			ID: c.id, ChatJID: "slack:C1", Sender: "u", Content: c.id, Timestamp: c.raw,
		})
	}

	dst := routdDB(t)
	if _, _, _, err := resreg.ImportMessagesJSONL(context.Background(), dst, bytes.NewReader(archiveOf(t, rows)), 0); err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, c := range cases {
		got := storedTS(t, dst, c.id)
		if got != core.TS(c.at) {
			t.Errorf("%s: archived %q stored as %q, want %q", c.id, c.raw, got, core.TS(c.at))
		}
		// One width for the whole column is what buys the ordering, so compare
		// against a rendering rather than the layout: in UTC `Z07:00` renders
		// as a single `Z`, which is shorter than the layout that spells it.
		if want := len(core.TS(time.Now())); len(got) != want {
			t.Errorf("%s: stored width %d, want the column's fixed %d", c.id, len(got), want)
		}
	}
}

// A value that is not an instant has no core.TS rendering, so it cannot be
// ordered and is never written. It is named instead: the restore completes, and
// correcting the row and re-running is the way back, because the import is
// idempotent on everything that already landed.
func TestImportRefusesTheUnorderableRowAndNamesIt(t *testing.T) {
	// The shape routd/migrations/0044 deliberately leaves alone rather than
	// invent a zone for. Two rows carry it on krons today.
	const handEdited = "2026-04-02 12:38:47"

	doc := archiveOf(t, []resreg.ArchiveMessageRow{
		{ID: "good-1", ChatJID: "slack:C1", Sender: "u", Content: "a", Timestamp: ts(t, "10:00")},
		{ID: "hand-edited", ChatJID: "slack:C1", Sender: "u", Content: "b", Timestamp: handEdited},
		{ID: "good-2", ChatJID: "slack:C1", Sender: "u", Content: "c", Timestamp: ts(t, "10:02")},
	})

	dst := routdDB(t)
	imported, touched, refused, err := resreg.ImportMessagesJSONL(context.Background(), dst, bytes.NewReader(doc), 0)
	if err != nil {
		t.Fatalf("one unorderable row failed the whole restore: %v", err)
	}
	if imported != 2 || countMessages(t, dst) != 2 {
		t.Errorf("imported %d, stored %d, want 2 and 2 — the rest of the archive must land", imported, countMessages(t, dst))
	}
	if len(refused) != 1 {
		t.Fatalf("refused = %v, want exactly the one unorderable row", refused)
	}
	if refused[0].ID != "hand-edited" || refused[0].Timestamp != handEdited {
		t.Errorf("refused[0] = %+v, want the id and the value the archive carried", refused[0])
	}
	var stored int
	if err := dst.QueryRow("SELECT COUNT(*) FROM messages WHERE id='hand-edited'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("the unorderable row was written; it sorts below every T-form row and hides behind any cursor")
	}

	// The way back: correct the row, re-run, and the archive completes without
	// duplicating what already landed.
	fixed := archiveOf(t, []resreg.ArchiveMessageRow{
		{ID: "good-1", ChatJID: "slack:C1", Sender: "u", Content: "a", Timestamp: ts(t, "10:00")},
		{ID: "hand-edited", ChatJID: "slack:C1", Sender: "u", Content: "b", Timestamp: ts(t, "10:01")},
		{ID: "good-2", ChatJID: "slack:C1", Sender: "u", Content: "c", Timestamp: ts(t, "10:02")},
	})
	if _, _, refused, err := resreg.ImportMessagesJSONL(context.Background(), dst, bytes.NewReader(fixed), 0); err != nil {
		t.Fatalf("re-run: %v", err)
	} else if len(refused) != 0 {
		t.Errorf("re-run refused %v", refused)
	}
	if got := countMessages(t, dst); got != 3 {
		t.Errorf("after the re-run the target holds %d rows, want 3", got)
	}
	if !touched["slack:C1"] {
		t.Error("the refused row's chat was dropped from the touched set")
	}
}

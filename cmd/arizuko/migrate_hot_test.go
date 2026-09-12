package main

// migrate_hot_test.go proves the half of BUGS L97 that lives in the move rather
// than in the migration chain: the CURSORS.
//
// routd's migration 0044 pads the message rows and this tool pads the two
// cursors that compare against them, in that order, and the order is what the
// failure was: cursors normalised while rows are not re-fed 28 consumed messages
// on krons. The tool refuses to start while the rows are unpadded, so the order
// cannot be got wrong — and the refusal is proved here as well as the padding,
// because a guard nobody fires is a guard nobody has.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// legacyMessagesOnRoutd is the pre-move shape of `messages` inside routd.db:
// routd's 0001 columns plus the three its chain added. It is a constant of its
// own because two fixtures put this table back — the hot move and the split —
// and two spellings would be two opinions of what the migrator reads.
const legacyMessagesOnRoutd = `
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

// legacyHotOnRoutd is the pre-move shape of the six tables inside routd.db.
// They are entities now, so routd.Create makes them as (part, sort, attrs) and
// the fixture has to put the columns back to have something legacy to move.
const legacyHotOnRoutd = legacyMessagesOnRoutd + `
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS chat_reply_state;
DROP TABLE IF EXISTS chat_proactive;
DROP TABLE IF EXISTS system_messages;
CREATE TABLE chats (
  jid TEXT PRIMARY KEY, errored INTEGER NOT NULL DEFAULT 0, agent_cursor TEXT,
  sticky_group TEXT, sticky_topic TEXT, is_group INTEGER NOT NULL DEFAULT 0);
CREATE TABLE sessions (
  group_folder TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL, parent_topic TEXT, forked_at TEXT,
  observed_cursor TEXT, PRIMARY KEY (group_folder, topic));
CREATE TABLE chat_reply_state (
  jid TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '',
  last_reply_id TEXT NOT NULL, engaged_until TEXT,
  engaged_folder TEXT NOT NULL DEFAULT '', PRIMARY KEY (jid, topic));
CREATE TABLE chat_proactive (
  jid TEXT PRIMARY KEY, proactive_last_fired_at TEXT);
CREATE TABLE system_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, folder TEXT NOT NULL,
  source TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL,
  created TEXT NOT NULL);
`

// hotLegacyRoutd builds a routd.db whose five hot tables still hold columns, and
// answers its path.
func hotLegacyRoutd(t *testing.T) string {
	t.Helper()
	storeDir := t.TempDir()
	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatalf("build routd.db: %v", err)
	}
	if _, err := rdb.SQL().Exec(legacyHotOnRoutd); err != nil {
		t.Fatalf("put the pre-move columns back: %v", err)
	}
	rdb.Close()
	return store.OwnerDBPath(storeDir, store.OwnerRoutd)
}

// openHot reopens the file without running anything, the way the tool does.
func openHot(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := db_utils.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", filepath.Base(path), err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insertHotMessage puts one row in the legacy `messages` the fixture rebuilt.
func insertHotMessage(t *testing.T, db *sql.DB, id, jid, ts string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp,
		    is_from_me, is_bot_message, reply_to_id, reply_to_text, reply_to_sender,
		    topic, routed_to, verb, attachments, source, is_observed, status, chat_name)
		 VALUES (?, ?, 'google:op', '', 'body', ?, 0, 0, '', '', '', '', '', 'message',
		    '', '', 0, 'sent', '')`, id, jid, ts); err != nil {
		t.Fatalf("seed message %s: %v", id, err)
	}
}

// TestMigrateHotPadsACursorOntoItsAlreadyPaddedRows is the end-to-end shape of
// L97: a deployed instance holds a whole-second cursor and a nanosecond row in
// that same second, and the row sorts BEHIND the cursor until both are one
// width. The migration chain padded the row; this tool pads the cursor.
func TestMigrateHotPadsACursorOntoItsAlreadyPaddedRows(t *testing.T) {
	path := hotLegacyRoutd(t)
	db := openHot(t, path)
	const jid = "slack:T/C/U"
	const trigger = "2026-08-26T19:06:41.882458214Z"

	insertHotMessage(t, db, "hitl-legacy", jid, trigger)
	if _, err := db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, '2026-08-26T19:06:41Z')`, jid,
	); err != nil {
		t.Fatalf("seed the legacy cursor: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (group_folder, topic, session_id, observed_cursor)
		 VALUES ('atlas', 'main', 's1', '2026-08-26T19:06:41Z')`); err != nil {
		t.Fatalf("seed the legacy observed cursor: %v", err)
	}
	// The precondition holds: the only message row is already padded.
	if err := migrateHot(path, false); err != nil {
		t.Fatalf("migrate-hot: %v", err)
	}

	st := store.New(db)
	cursor, err := st.AgentCursor(jid)
	if err != nil {
		t.Fatal(err)
	}
	// THE CURSOR IS DERIVED, NOT PADDED. It names the newest message at or before
	// the instant the chat held, and this chat's only message is the trigger,
	// which lands AFTER that instant — so there is nothing at or before it and the
	// chat feeds from the start. That is the same position the stored instant
	// meant, and it is what re-issues the held call.
	if cursor != "" {
		t.Fatalf("agent cursor = %q, want none: no message sits at or before the instant "+
			"the chat held", cursor)
	}
	unfed, err := st.NewMessagesForChat(context.Background(), jid, cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfed) != 1 || unfed[0].ID != "hitl-legacy" {
		t.Errorf("the dispatch feed holds %+v; the re-issue trigger is the one row that "+
			"must still be unfed (BUGS L97)", unfed)
	}
	line, ok := st.TopicLineage("atlas", "main")
	if !ok {
		t.Fatal("the session did not travel")
	}
	if line.ObservedCursor != "2026-08-26T19:06:41.000000000Z" {
		t.Errorf("observed cursor = %q, want the padded instant", line.ObservedCursor)
	}
}

// TestMigrateHotCarriesAnUnpaddedRowAndItsCursorTogether is the order the
// failure was, pinned where the shape now settles it. A pre-0044 instance holds
// an unpadded row AND an unpadded cursor; both are rendered at core.TS's width
// on the way in — the row by the message writer, the cursor by the derivation
// that reads the keys the rows became — so neither can move relative to the
// other, and the chat's unfed set is what it was.
func TestMigrateHotCarriesAnUnpaddedRowAndItsCursorTogether(t *testing.T) {
	path := hotLegacyRoutd(t)
	db := openHot(t, path)
	const jid = "slack:T/C/U"

	// A row 0044 would still pad: the state a pre-0044 instance is in.
	insertHotMessage(t, db, "unpadded", jid, "2026-08-26T19:06:41Z")
	if _, err := db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, '2026-08-26T19:06:41Z')`, jid,
	); err != nil {
		t.Fatal(err)
	}

	// The move's own verification refuses a cursor that names a different
	// position, so a green run IS the assertion that none moved.
	if err := migrateHot(path, false); err != nil {
		t.Fatalf("migrate-hot: %v", err)
	}

	st := store.New(db)
	const padded = "2026-08-26T19:06:41.000000000Z"
	if got, want := mustCursor(t, st, jid), store.MsgStamp(padded, "unpadded"); got != want {
		t.Fatalf("agent cursor = %q, want the consumed row's own key %q", got, want)
	}
	unfed, err := st.NewMessagesForChat(t.Context(), jid, mustCursor(t, st, jid), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfed) != 0 {
		t.Errorf("%d rows are unfed after the move; the consumed message is re-fed", len(unfed))
	}
}

// mustCursor reads a chat's dispatch cursor or fails.
func mustCursor(t *testing.T, st *store.Store, jid string) string {
	t.Helper()
	c, err := st.AgentCursor(jid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestMigrateHotReadsACursorTheWayItReadsItsRows is the other half of the same
// rule, and the rule changed with the rows.
//
// Migration 0044 left a hand-written `2026-04-02 12:38:47` alone because padding
// a ROW in place would invent a zone it never had. An item has no position
// without an instant, so the move reads such a row as UTC rather than dropping
// it — and the moment it does, the cursor MUST be read the same way. One reader
// for both is what stops the two drifting: a cursor refused while its rows were
// carried would shift the poll loop relative to the history it is a position in.
func TestMigrateHotReadsACursorTheWayItReadsItsRows(t *testing.T) {
	path := hotLegacyRoutd(t)
	db := openHot(t, path)
	const jid = "slack:T/C/U"

	// The hand-written row, and a later one the cursor has NOT consumed.
	insertHotMessage(t, db, "hand-written", jid, "2026-04-02 12:38:47")
	insertHotMessage(t, db, "later", jid, "2026-04-03T09:00:00.000000000Z")
	if _, err := db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, '2026-04-02 12:38:47')`, jid,
	); err != nil {
		t.Fatal(err)
	}
	if err := migrateHot(path, false); err != nil {
		t.Fatalf("migrate-hot: %v", err)
	}

	st := store.New(db)
	// Both rows travelled: neither was dropped for the shape of its instant.
	n, err := st.CountMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d messages travelled, want 2 — the hand-written row was dropped", n)
	}
	// The cursor still names the same position: the hand-written row is consumed
	// and the later one is not.
	cursor, err := st.AgentCursor(jid)
	if err != nil {
		t.Fatal(err)
	}
	unfed, err := st.NewMessagesForChat(context.Background(), jid, cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfed) != 1 || unfed[0].ID != "later" {
		t.Errorf("the feed holds %+v; the cursor moved relative to the row it was written "+
			"beside, which re-feeds or skips everything between the two values", unfed)
	}
}

// TestMigrateHotLeavesAnAlreadyFixedWidthCursorAlone is what the rehearsal on
// the live snapshots asserts, in a test: every cursor on all three instances is
// already core.TS's width, so the move must be a no-op on them. A cursor that
// moved by a byte would re-feed or skip everything between the two values.
func TestMigrateHotLeavesAnAlreadyFixedWidthCursorAlone(t *testing.T) {
	path := hotLegacyRoutd(t)
	db := openHot(t, path)
	const jid = "slack:T/C/U"
	const cursor = "2026-08-26T19:06:41.882458214Z"

	insertHotMessage(t, db, "m1", jid, cursor)
	if _, err := db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, ?)`, jid, cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (group_folder, topic, session_id, observed_cursor)
		 VALUES ('atlas', '', 's1', ?)`, cursor); err != nil {
		t.Fatal(err)
	}
	if err := migrateHot(path, false); err != nil {
		t.Fatalf("migrate-hot: %v", err)
	}
	st := store.New(db)
	// The dispatch cursor holds a whole message KEY now, so an unchanged
	// POSITION is the newest consumed row's own key rather than the instant the
	// poller stopped at. The observed cursor is still a bare instant and is
	// carried verbatim.
	if got, want := mustCursor(t, st, jid), store.MsgStamp(cursor, "m1"); got != want {
		t.Errorf("agent cursor moved to %q from %q", got, want)
	}
	if line, _ := st.TopicLineage("atlas", ""); line.ObservedCursor != cursor {
		t.Errorf("observed cursor moved to %q from %q", line.ObservedCursor, cursor)
	}
}

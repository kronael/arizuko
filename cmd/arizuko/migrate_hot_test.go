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
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// legacyHotOnRoutd is the pre-move shape of the five tables inside routd.db.
// They are entities now, so routd.Create makes them as (part, sort, attrs) and
// the fixture has to put the columns back to have something legacy to move.
const legacyHotOnRoutd = `
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
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		t.Fatalf("open %s: %v", filepath.Base(path), err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insertHotMessage puts one row in `messages`, which stays a table of columns.
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
	if cursor != "2026-08-26T19:06:41.000000000Z" {
		t.Fatalf("agent cursor = %q, want the padded instant", cursor)
	}
	if !(trigger > cursor) {
		t.Errorf("the trigger %q still sorts behind the cursor %q; no held call is ever "+
			"re-issued (BUGS L97)", trigger, cursor)
	}
	line, ok := st.TopicLineage("atlas", "main")
	if !ok {
		t.Fatal("the session did not travel")
	}
	if line.ObservedCursor != "2026-08-26T19:06:41.000000000Z" {
		t.Errorf("observed cursor = %q, want the padded instant", line.ObservedCursor)
	}
}

// TestMigrateHotRefusesToMoveACursorAheadOfItsRows is the guard, and it is the
// order the failure was. Take requirePaddedMessageRows out of migrate_hot.go and
// this goes green while the cursor lands padded beside an unpadded row it now
// sorts after — the 28 messages krons re-fed, in one test.
func TestMigrateHotRefusesToMoveACursorAheadOfItsRows(t *testing.T) {
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

	err := migrateHot(path, false)
	if err == nil {
		t.Fatal("the move ran with unpadded message rows; a padded cursor sorts before " +
			"them and re-feeds every consumed message in its own second")
	}
	if !containsAll(err.Error(), "0044", "message rows") {
		t.Errorf("the refusal does not name the migration that pads the rows: %v", err)
	}
	// Nothing was set aside, so a re-run after the migration finds its source.
	var legacy int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chats_v1'`).
		Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 {
		t.Error("the refused run still set the legacy table aside")
	}
}

// TestMigrateHotRefusesACursorShapeTheRowsDoNotShare is the other half of the
// same rule. Migration 0044 leaves a hand-written `2026-04-02 12:38:47` alone
// because padding it would invent a zone it never had — so a cursor of that
// shape has to be left alone too, and a cursor this move cannot leave alone is
// one it would shift relative to a row it did not touch.
func TestMigrateHotRefusesACursorShapeTheRowsDoNotShare(t *testing.T) {
	path := hotLegacyRoutd(t)
	db := openHot(t, path)
	const jid = "slack:T/C/U"

	insertHotMessage(t, db, "hand-written", jid, "2026-04-02 12:38:47")
	if _, err := db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, '2026-04-02 12:38:47')`, jid,
	); err != nil {
		t.Fatal(err)
	}
	err := migrateHot(path, false)
	if err == nil {
		t.Fatal("a cursor of a shape the rows do not share was carried")
	}
	if !containsAll(err.Error(), "agent_cursor", "0044") {
		t.Errorf("the refusal does not name the attribute and the migration: %v", err)
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
	if got, _ := st.AgentCursor(jid); got != cursor {
		t.Errorf("agent cursor moved to %q from %q", got, cursor)
	}
	if line, _ := st.TopicLineage("atlas", ""); line.ObservedCursor != cursor {
		t.Errorf("observed cursor moved to %q from %q", line.ObservedCursor, cursor)
	}
}

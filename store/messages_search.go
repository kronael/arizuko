package store

// messages_search.go is ranked search: the FTS5 index over a message's content,
// the three triggers that fill it from the entity, and the one query that reads
// it back.
//
// IT IS STILL SQL AND STILL FTS5. No document store answers bm25 and no
// denormalisation produces one, so ranked search leaves the store for its own
// daemon with a tailing index rather than becoming an entity (spec 6/37
// §"Search is the one that is answered by leaving"). Until that daemon exists it
// stays here, and it is the ONE hand-written statement in this package that
// reaches into an entity's document.
//
// THE INDEX CARRIES THE MESSAGE'S OWN COORDINATE AND THE JOIN READS THAT. An
// FTS5 row is addressed by a rowid and by nothing else, so the index mirrors the
// entity's rowid to give its own triggers a point delete — measured at 1.2ms
// against 31ms for the same delete matched on the coordinate over 24,013 rows.
// That rowid is private to the triggers. What the JOIN reads is `part` and
// `sort`, the two UNINDEXED columns the trigger copies out of the row it indexes,
// so an index row can only ever be reported under the message it was made from.
//
// The rowid would answer the same question only while the two tables agree about
// what sits at each one, and nothing makes them. The index is filled by the
// schema chain over the legacy table and by this file's backfill over the entity,
// and the orders those two produce line up by accident: `arizuko migrate-hot`
// reads the legacy rows in rowid order and writes them in that order, so on the
// krons snapshot 0 of 24,013 rowids name a different message after the move. A
// refused row, a re-run or a restore onto a target that already holds part of the
// history shifts every rowid after it, and then a rowid join answers with
// whichever message now sits there. That hit carries the wrong chat, the ACL
// filter resolves the ROUTE OF THAT CHAT (`ipc.find_messages`), and a folder that
// never saw the message is handed its snippet.
//
// WHAT STILL BLOCKS routd ON POSTGRES. FTS5 is a SQLite extension, so
// `messages_fts`, its three triggers, `snippet()` and `bm25()` have no Postgres
// spelling, and `json_extract(attrs, '$.x')` is `attrs->>'x'` there. This file is
// SQLite-only, and so is `routd/migrations` — the chain that already holds routd
// to SQLite. Nothing here widens that hold and nothing here narrows it. The
// search daemon is what ends it, by taking the index out of the owner's database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/groupfolder"
)

// FoundMessage is one row of FindMessages output. Content is the
// snippet returned by FTS5's `snippet()` (matched fragment with
// «»-highlighting around the hit), not the full message body.
// Rank is BM25 — lower is a better match.
type FoundMessage struct {
	ChatJID      string    `json:"chat_jid"`
	Sender       string    `json:"sender"`
	Timestamp    time.Time `json:"timestamp"`
	IsFromMe     bool      `json:"is_from_me"`
	IsBotMessage bool      `json:"is_bot_message"`
	Content      string    `json:"content"`
	Rank         float64   `json:"rank"`
}

// searchIndex is the name the index answers to. The reader below and the DDL
// share it, so it is written once.
const searchIndex = "messages_fts"

// indexColumns and indexValues are the index row: what a trigger writes and
// what the backfill writes, from one declaration, because two spellings of one
// row are two things that can disagree about what an index row holds.
//
// `rowid` is the entity's, which is what makes the delete a point operation —
// FTS5 offers no other address. The file header says why the join does not read
// it.
const (
	indexColumns = "rowid, content, part, sort"
	indexValues  = "new.rowid, json_extract(new.attrs, '$." + attrMsgContent + "'), new.part, new.sort"
)

// messageSearchDDL is the index and its three triggers, in creation order, each
// under the name sqlite_master holds it by. The `create` text is compared
// against sqlite_master's own, so this declaration IS the shape check.
var messageSearchDDL = []struct{ name, drop, create string }{
	{
		name: searchIndex,
		drop: "DROP TABLE IF EXISTS " + searchIndex,
		create: "CREATE VIRTUAL TABLE " + searchIndex + " USING fts5(" +
			"content, part UNINDEXED, sort UNINDEXED, " +
			"tokenize='unicode61 remove_diacritics 2')",
	},
	{
		name: searchIndex + "_ai",
		drop: "DROP TRIGGER IF EXISTS " + searchIndex + "_ai",
		create: "CREATE TRIGGER " + searchIndex + "_ai AFTER INSERT ON " + Messages + " BEGIN " +
			"INSERT INTO " + searchIndex + "(" + indexColumns + ") VALUES (" + indexValues + "); END",
	},
	{
		name: searchIndex + "_ad",
		drop: "DROP TRIGGER IF EXISTS " + searchIndex + "_ad",
		create: "CREATE TRIGGER " + searchIndex + "_ad AFTER DELETE ON " + Messages + " BEGIN " +
			"DELETE FROM " + searchIndex + " WHERE rowid = old.rowid; END",
	},
	{
		name: searchIndex + "_au",
		drop: "DROP TRIGGER IF EXISTS " + searchIndex + "_au",
		create: "CREATE TRIGGER " + searchIndex + "_au AFTER UPDATE ON " + Messages + " BEGIN " +
			"DELETE FROM " + searchIndex + " WHERE rowid = old.rowid; " +
			"INSERT INTO " + searchIndex + "(" + indexColumns + ") VALUES (" + indexValues + "); END",
	},
}

// EnsureMessageSearch brings one database's message index to the shape this file
// reads, and it is where that shape is repaired.
//
// It is a call of its own rather than a member of EnsureEntities because the
// index is not an entity: it is SQLite's, it is declared here beside the query
// that reads it, and a trigger cannot be declared until the entity it fires on
// EXISTS. So it runs where SeedRoutdFloor runs — after the driver has made the
// entities — in routd's open, in store's own migrate and at the end of
// `arizuko migrate-hot`.
//
// IT REBUILDS RATHER THAN PATCHES. A database born on a schema chain carries an
// index over a table of columns; a database `arizuko migrate-hot` has moved
// carries that same index, still holding the legacy table's rowids, over a table
// that is now the entity. Neither can be edited into this shape, and an index
// whose rows point at another table's rowids is the one thing the join must
// never be handed. So the check is exact — all four statements read back from
// sqlite_master, byte for byte — and anything else is dropped and built again
// from the rows the entity holds. A database already in shape does four reads
// and writes nothing, which is what lets every opener call it on every boot.
func EnsureMessageSearch(ctx context.Context, db *sql.DB) error {
	current, err := messageSearchCurrent(ctx, db)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	// Reverse order: a trigger names the index, so the index goes last.
	for i := len(messageSearchDDL) - 1; i >= 0; i-- {
		if _, err := db.ExecContext(ctx, messageSearchDDL[i].drop); err != nil {
			return fmt.Errorf("drop %s: %w", messageSearchDDL[i].name, err)
		}
	}
	for _, d := range messageSearchDDL {
		if _, err := db.ExecContext(ctx, d.create); err != nil {
			return fmt.Errorf("create %s: %w", d.name, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+searchIndex+"("+indexColumns+") SELECT rowid, "+
			"json_extract(attrs, '$."+attrMsgContent+"'), part, sort FROM "+Messages); err != nil {
		return fmt.Errorf("fill %s from %s: %w", searchIndex, Messages, err)
	}
	return nil
}

// messageSearchCurrent reports whether all four statements are already in the
// database, exactly as declared. A missing one and a differing one are the same
// answer: the index is rebuilt either way, and a half-current index is the state
// that has no repair.
func messageSearchCurrent(ctx context.Context, db *sql.DB) (bool, error) {
	for _, d := range messageSearchDDL {
		var got string
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(sql, '') FROM sqlite_master WHERE name = ?", d.name).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read the declaration of %s: %w", d.name, err)
		}
		if strings.TrimSpace(got) != d.create {
			return false, nil
		}
	}
	return true, nil
}

// msgAttr renders one attribute of the joined row. An absent attribute reads as
// the empty string, which is what absence MEANS here: setAttr omits an empty
// value and setFlag omits a false one, so a NULL would only be a second spelling
// of the same fact — and one the Go scan cannot take.
func msgAttr(name string) string {
	return "COALESCE(json_extract(m.attrs, '$." + name + "'), '')"
}

// msgInstant is the instant half of a message's position. The message's time is
// in the COORDINATE and not among its attributes, so the read takes it from
// there; every key is written through MsgStamp, whose lead is core.TS at a fixed
// width, so a fixed-width prefix is the whole instant and nothing else.
var msgInstant = fmt.Sprintf("substr(m.sort, 1, %d)", tsWidth)

// FindMessages runs an FTS5 MATCH against messages_fts and joins back to the
// message entity for metadata. `query` is bound as a parameter — FTS5 parses it as
// its own syntax, so no SQL escape is needed; a malformed query surfaces
// SQLITE_ERROR to the caller. ACL filtering is the caller's job.
//
// `scope` is polymorphic: a string containing ':' matches the chat, otherwise it is a
// folder subtree on `routed_to`. `sender` is exact match; `since` is an RFC3339 lower
// bound (inclusive).
func (s *Store) FindMessages(query, scope, sender, since string, limit int) ([]FoundMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var scopeJID any
	var scopeFolder string
	if scope != "" {
		if strings.Contains(scope, ":") {
			scopeJID = scope
		} else {
			scopeFolder = scope
		}
	}
	var senderArg any
	if sender != "" {
		senderArg = sender
	}
	var sinceArg any
	if since != "" {
		sinceArg = since
	}
	rows, err := s.db.Query(
		`SELECT m.part, `+msgAttr(attrMsgSender)+`, `+msgInstant+`,
		        `+msgAttr(attrMsgFromMe)+`, `+msgAttr(attrMsgBot)+`,
		        snippet(`+searchIndex+`, 0, '«', '»', '…', 32) AS content,
		        bm25(`+searchIndex+`) AS rank
		 FROM `+searchIndex+`
		 JOIN `+Messages+` m
		   ON m.part = `+searchIndex+`.part AND m.sort = `+searchIndex+`.sort
		 WHERE `+searchIndex+` MATCH ?
		   AND (? IS NULL OR m.part = ?)
		   AND (? = '' OR `+groupfolder.SQLSubtree(msgAttr(attrMsgRoutedTo), "/")+`)
		   AND (? IS NULL OR `+msgAttr(attrMsgSender)+` = ?)
		   AND (? IS NULL OR `+msgInstant+` >= ?)
		 ORDER BY rank, m.sort DESC
		 LIMIT ?`,
		append(append([]any{query, scopeJID, scopeJID, scopeFolder},
			groupfolder.SubtreeArgs(scopeFolder, "/")...),
			senderArg, senderArg, sinceArg, sinceArg, limit)...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoundMessage
	for rows.Next() {
		var f FoundMessage
		var ts, fromMe, botMsg string
		if err := rows.Scan(&f.ChatJID, &f.Sender, &ts,
			&fromMe, &botMsg, &f.Content, &f.Rank); err != nil {
			return nil, err
		}
		f.IsFromMe = fromMe == flagSet
		f.IsBotMessage = botMsg == flagSet
		f.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, f)
	}
	return out, rows.Err()
}

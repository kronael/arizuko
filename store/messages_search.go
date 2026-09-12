package store

// messages_search.go is the ranked search read, and it is the ONE thing the
// messages move did not carry.
//
// IT IS STILL SQL ON PURPOSE. No document store answers bm25 and no
// denormalisation produces one, so ranked search leaves the store for its own
// daemon with a tailing index rather than becoming an entity (spec 6/37
// §"Search is the one that is answered by leaving"). Until that daemon exists
// this stays where it was, untouched, so the move that carries everything else
// is not also a search rewrite.
//
// WHAT THE MOVE COSTS IT, MEASURED. `messages_fts`' three triggers are declared
// ON `messages`, and an entity takes that table's name: on a fresh instance
// sqlstore.CreateEntity drops the empty legacy table and the triggers go with it,
// and on a migrated one `arizuko migrate-hot` renames the table and SQLite
// rewrites the triggers to follow it. Either way the join below meets a table of
// (part, sort, attrs) and fails with "no such column: m.chat_jid". That is LOUD,
// which is the one thing to be glad of — the alternative, a join that compiled,
// would have matched an FTS rowid against an unrelated item's rowid and returned
// one chat's snippet under another chat's `routed_to`, which is what the ACL
// filter reads.

import (
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

// FindMessages runs an FTS5 MATCH against messages_fts and joins back to messages for
// metadata. `query` is bound as a parameter — FTS5 parses it as its own syntax, so no
// SQL escape is needed; a malformed query surfaces SQLITE_ERROR to the caller. ACL
// filtering is the caller's job.
//
// `scope` is polymorphic: a string containing ':' matches chat_jid, otherwise it is a
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
		`SELECT m.chat_jid, m.sender, m.timestamp, m.is_from_me, m.is_bot_message,
		        snippet(messages_fts, 0, '«', '»', '…', 32) AS content,
		        bm25(messages_fts) AS rank
		 FROM messages_fts f
		 JOIN messages m ON m.rowid = f.rowid
		 WHERE messages_fts MATCH ?
		   AND (? IS NULL OR m.chat_jid = ?)
		   AND (? = '' OR `+groupfolder.SQLSubtree("m.routed_to", "/")+`)
		   AND (? IS NULL OR m.sender = ?)
		   AND (? IS NULL OR m.timestamp >= ?)
		 ORDER BY rank, m.timestamp DESC
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
		var ts string
		var fromMe, botMsg int
		if err := rows.Scan(&f.ChatJID, &f.Sender, &ts,
			&fromMe, &botMsg, &f.Content, &f.Rank); err != nil {
			return nil, err
		}
		f.IsFromMe = fromMe != 0
		f.IsBotMessage = botMsg != 0
		f.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, f)
	}
	return out, rows.Err()
}

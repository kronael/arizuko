package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
)

func (s *Store) PutMessage(m core.Message) error {
	if m.Topic == "" {
		m.Topic = s.GetStickyTopic(m.ChatJID)
	}
	if m.Status == "" {
		m.Status = core.MessageStatusSent
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message, forwarded_from,
		  reply_to_id, reply_to_text, reply_to_sender, topic, routed_to, verb, attachments, source, turn_id, status, chat_name, platform_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content,
		core.TS(m.Timestamp),
		btoi(m.FromMe), btoi(m.BotMsg),
		nilIfEmpty(m.ForwardedFrom),
		nilIfEmpty(m.ReplyToID), nilIfEmpty(m.ReplyToText), nilIfEmpty(m.ReplyToSender),
		m.Topic, m.RoutedTo, m.Verb, m.Attachments, m.Source, nilIfEmpty(m.TurnID), m.Status, m.ChatName, nilIfEmpty(m.PlatformID),
	)
	return err
}

func (s *Store) EnrichMessage(id, content string) error {
	_, err := s.db.Exec(
		`UPDATE messages SET content=?, attachments='' WHERE id=?`,
		content, id,
	)
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const msgCols = `id, chat_jid, sender, COALESCE(sender_name,''), content, timestamp,
	is_from_me, is_bot_message, COALESCE(forwarded_from,''),
	COALESCE(reply_to_id,''), COALESCE(reply_to_text,''), COALESCE(reply_to_sender,''),
	topic, routed_to, verb, attachments, source, errored, COALESCE(turn_id,''), COALESCE(chat_name,''), COALESCE(platform_id,'')`

func (s *Store) NewMessages(jids []string, since time.Time, botName string) ([]core.Message, time.Time, error) {
	var rows *sql.Rows
	var err error
	if len(jids) == 0 {
		rows, err = s.db.Query(
			`SELECT `+msgCols+` FROM messages
			 WHERE timestamp > ?
			   AND is_bot_message = 0
			   AND sender NOT LIKE ?
			 ORDER BY timestamp ASC
			 LIMIT 200`,
			core.TS(since), botName+"%",
		)
	} else {
		args := make([]any, 0, len(jids)+2)
		for _, jid := range jids {
			args = append(args, jid)
		}
		args = append(args, core.TS(since), botName+"%")
		ph := "(" + strings.TrimSuffix(strings.Repeat("?,", len(jids)), ",") + ")"
		rows, err = s.db.Query(
			`SELECT `+msgCols+` FROM messages
			 WHERE chat_jid IN `+ph+`
			   AND timestamp > ?
			   AND is_bot_message = 0
			   AND sender NOT LIKE ?
			 ORDER BY timestamp ASC
			 LIMIT 200`,
			args...,
		)
	}
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()

	var msgs []core.Message
	hi := since
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, since, err
		}
		msgs = append(msgs, m)
		if m.Timestamp.After(hi) {
			hi = m.Timestamp
		}
	}
	return msgs, hi, rows.Err()
}

func (s *Store) MarkMessagesErrored(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	q := `UPDATE messages SET errored = 1 WHERE id IN (?` +
		strings.Repeat(",?", len(ids)-1) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.db.Exec(q, args...)
	return err
}

func (s *Store) DeleteErroredMessages(chatJid string) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE chat_jid = ? AND errored = 1`, chatJid)
	return err
}

func (s *Store) MessagesSince(jid string, since time.Time, botName string) ([]core.Message, error) {
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid = ?
		   AND timestamp > ?
		   AND is_bot_message = 0
		   AND sender NOT LIKE ?
		 ORDER BY timestamp ASC
		 LIMIT 100`,
		jid, core.TS(since), botName+"%",
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}

// BotRepliesSince returns the agent's outbound messages on a chat JID after a
// timestamp, oldest first — the inverse filter of MessagesSince (which excludes
// bot rows). Used by `arizuko send` operator-direct mode to surface the agent's
// reply to a CLI-injected message.
func (s *Store) BotRepliesSince(jid string, since time.Time) ([]core.Message, error) {
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid = ?
		   AND timestamp > ?
		   AND is_bot_message = 1
		 ORDER BY timestamp ASC
		 LIMIT 100`,
		jid, core.TS(since),
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func collectMessages(rows *sql.Rows) ([]core.Message, error) {
	defer rows.Close()
	var msgs []core.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func scanMessage(r rowScanner) (core.Message, error) {
	var m core.Message
	var ts string
	var fromMe, botMsg, errored int
	if err := r.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Name, &m.Content,
		&ts, &fromMe, &botMsg, &m.ForwardedFrom,
		&m.ReplyToID, &m.ReplyToText, &m.ReplyToSender,
		&m.Topic, &m.RoutedTo, &m.Verb, &m.Attachments, &m.Source, &errored, &m.TurnID, &m.ChatName, &m.PlatformID); err != nil {
		return m, err
	}
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Errored = errored != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, nil
}

type TopicSummary struct {
	ID           string
	Preview      string
	LastAt       time.Time
	MessageCount int
}

func (s *Store) Topics(folder string) ([]TopicSummary, error) {
	jid := "web:" + folder
	rows, err := s.db.Query(
		`SELECT topic,
		        substr(MIN(content),1,80) AS preview,
		        MAX(timestamp) AS last_at,
		        COUNT(*) AS cnt
		 FROM messages
		 WHERE chat_jid = ? AND topic != ''
		 GROUP BY topic
		 ORDER BY last_at DESC
		 LIMIT 100`,
		jid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicSummary
	for rows.Next() {
		var t TopicSummary
		var lastAt string
		rows.Scan(&t.ID, &t.Preview, &lastAt, &t.MessageCount)
		t.LastAt, _ = time.Parse(time.RFC3339Nano, lastAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) MessagesByTopic(folder, topic string, before time.Time, limit int) ([]core.Message, error) {
	return s.MessagesByThread("web:"+folder, topic, before, limit)
}

func (s *Store) MessagesByThread(jid, topic string, before time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before.IsZero() {
		before = time.Now()
	}
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid = ? AND topic = ? AND timestamp < ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		jid, topic, core.TS(before), limit,
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}

func (s *Store) TopicByMessageID(id, jid string) string {
	var topic string
	s.db.QueryRow(`SELECT COALESCE(topic,'') FROM messages WHERE id=? AND chat_jid=?`,
		id, jid).Scan(&topic)
	return topic
}

func (s *Store) MessageTimestampByID(id, jid string) (time.Time, bool) {
	var ts string
	err := s.db.QueryRow(`SELECT timestamp FROM messages WHERE id=? AND chat_jid=?`,
		id, jid).Scan(&ts)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, err == nil
}

func (s *Store) MessagesSinceTopic(jid, topic string, after time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid = ? AND topic = ? AND timestamp > ?
		 ORDER BY timestamp ASC
		 LIMIT ?`,
		jid, topic, core.TS(after), limit,
	)
	if err != nil {
		return nil, err
	}
	return collectMessages(rows)
}

// MarkMessagesObserved records ids as ambient context for folder — the poll
// loop calls it when the resolved route target is `folder#observe`.
func (s *Store) MarkMessagesObserved(folder string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	q := `UPDATE messages SET is_observed = 1, routed_to = ? WHERE id IN (?` +
		strings.Repeat(",?", len(ids)-1) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, folder)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Exec(q, args...)
	return err
}

// ObservedSince returns the trailing window of observed messages for folder whose
// timestamp is strictly greater than cursor. Empty cursor means no lower bound.
// Capped by maxMsgs and maxChars; older messages drop first when the char cap binds.
// Per spec 6/F the caller advances the topic's observed_cursor after rendering.
func (s *Store) ObservedSince(folder, cursor string, maxMsgs, maxChars int) []core.Message {
	if maxMsgs <= 0 || maxChars <= 0 {
		return nil
	}
	// Spec 6/F ambient join: open siblings widen routed_to for is_observed=1 rows.
	siblings := s.SiblingFolders(folder)
	// observe_group: watched source folders contribute their primary-delivery
	// messages (is_observed=0) as additional ambient context for the observer.
	watched := s.WatchedSources(folder)

	observed := append([]string{folder}, siblings...)
	observedPH := "?" + strings.Repeat(",?", len(observed)-1)
	args := make([]any, 0, len(observed)+len(watched)+3)
	for _, f := range observed {
		args = append(args, f)
	}

	cursorClause := ""
	if cursor != "" {
		cursorClause = " AND timestamp > ?"
	}

	var q string
	if len(watched) > 0 {
		watchedPH := "?" + strings.Repeat(",?", len(watched)-1)
		// Arg order must match placeholder order: observed folders, then the
		// first SELECT's cursor, then watched folders, then the second
		// SELECT's cursor, then LIMIT.
		if cursor != "" {
			args = append(args, cursor)
		}
		for _, f := range watched {
			args = append(args, f)
		}
		if cursor != "" {
			args = append(args, cursor)
		}
		args = append(args, maxMsgs)
		q = `SELECT ` + msgCols + ` FROM messages
		     WHERE routed_to IN (` + observedPH + `) AND is_observed = 1
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     UNION ALL
		     SELECT ` + msgCols + ` FROM messages
		     WHERE routed_to IN (` + watchedPH + `) AND is_observed = 0
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     ORDER BY timestamp DESC
		     LIMIT ?`
	} else {
		if cursor != "" {
			args = append(args, cursor)
		}
		args = append(args, maxMsgs)
		q = `SELECT ` + msgCols + ` FROM messages
		     WHERE routed_to IN (` + observedPH + `) AND is_observed = 1
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     ORDER BY timestamp DESC
		     LIMIT ?`
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	rev, err := collectMessages(rows)
	if err != nil {
		return nil
	}
	chars := 0
	cut := 0
	for i, m := range rev {
		c := len(m.Content)
		if chars+c > maxChars {
			break
		}
		chars += c
		cut = i + 1
	}
	rev = rev[:cut]
	out := make([]core.Message, len(rev))
	for i, m := range rev {
		out[len(rev)-1-i] = m
	}
	return out
}

func (s *Store) GetLastReplyID(jid, topic string) string {
	var id string
	s.db.QueryRow(
		`SELECT last_reply_id FROM chat_reply_state WHERE jid=? AND topic=?`,
		jid, topic,
	).Scan(&id)
	return id
}

// SetLastReply upserts last_reply_id AND engaged_folder for (jid, topic). Called by
// every conversational outbound site; the engagement bump is a separate call so
// timed-* turns can skip it while still recording the last reply (spec 5/G).
//
// Empty replyID leaves last_reply_id unchanged on existing rows. folder is recorded
// as engaged_folder so EngagedFolder() is a single-row read independent of any prior
// reply having succeeded.
func (s *Store) SetLastReply(jid, topic, replyID, folder string) error {
	_, err := s.db.Exec(
		`INSERT INTO chat_reply_state (jid, topic, last_reply_id, engaged_folder)
		 VALUES (?,?,?,?)
		 ON CONFLICT(jid, topic) DO UPDATE SET
		   last_reply_id = CASE WHEN excluded.last_reply_id != ''
		                        THEN excluded.last_reply_id
		                        ELSE chat_reply_state.last_reply_id END,
		   engaged_folder = CASE WHEN excluded.engaged_folder != ''
		                         THEN excluded.engaged_folder
		                         ELSE chat_reply_state.engaged_folder END`,
		jid, topic, replyID, folder,
	)
	return err
}

// BumpEngagement upserts engaged_until and engaged_folder for (jid, topic).
// Called only by sites that know the trigger sender is not timed-*; the
// timed-skip policy lives at the call sites (spec 5/G: scheduled / autonomous
// turns must not extend engagement). Zero `until` clears (writes NULL).
// `folder` is the bot folder claiming the conversation.
func (s *Store) BumpEngagement(jid, topic, folder string, until time.Time) error {
	var engaged any
	if !until.IsZero() {
		engaged = core.TS(until)
	}
	_, err := s.db.Exec(
		`INSERT INTO chat_reply_state (jid, topic, last_reply_id, engaged_until, engaged_folder)
		 VALUES (?,?,'',?,?)
		 ON CONFLICT(jid, topic) DO UPDATE SET
		   engaged_until = excluded.engaged_until,
		   engaged_folder = CASE WHEN excluded.engaged_folder != ''
		                         THEN excluded.engaged_folder
		                         ELSE chat_reply_state.engaged_folder END`,
		jid, topic, engaged, folder,
	)
	return err
}

// SetEngagement is BumpEngagement under the name the mention write site and
// the engage/disengage MCP tools call it by.
func (s *Store) SetEngagement(jid, topic, folder string, until time.Time) error {
	return s.BumpEngagement(jid, topic, folder, until)
}

// IsEngaged returns true when (jid, topic) has a non-NULL engaged_until
// strictly after `now`. Missing row → false.
func (s *Store) IsEngaged(jid, topic string, now time.Time) bool {
	var s_ string
	err := s.db.QueryRow(
		`SELECT COALESCE(engaged_until,'') FROM chat_reply_state WHERE jid=? AND topic=?`,
		jid, topic,
	).Scan(&s_)
	if err != nil || s_ == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, s_)
	if err != nil {
		return false
	}
	return t.After(now)
}

// EngagedFolder returns the folder claiming (jid, topic), or "" if none.
// Reads engaged_folder directly (single row, no JOIN) so the value survives
// turn-by-turn rewrites and is correct even when verb=mention engaged the
// pair before any bot reply has succeeded.
func (s *Store) EngagedFolder(jid, topic string) string {
	var f string
	s.db.QueryRow(
		`SELECT COALESCE(engaged_folder,'') FROM chat_reply_state WHERE jid=? AND topic=?`,
		jid, topic,
	).Scan(&f)
	return f
}

func (s *Store) RoutedToByMessageID(id string) string {
	var routedTo string
	s.db.QueryRow(`SELECT COALESCE(routed_to,'') FROM messages WHERE id=? AND routed_to!=''`,
		id).Scan(&routedTo)
	return routedTo
}

// MessageByID returns one message by primary key, or ok=false if missing.
// Light-weight read for tests and probes; production routing paths use
// the bulk `NewMessages` window instead.
func (s *Store) MessageByID(id string) (core.Message, bool) {
	if id == "" {
		return core.Message{}, false
	}
	var m core.Message
	var ts string
	var fromMe, botMsg int
	err := s.db.QueryRow(`
		SELECT id, chat_jid, sender, COALESCE(sender_name,''), content, timestamp,
		       is_from_me, is_bot_message, COALESCE(reply_to_id,''),
		       topic, COALESCE(routed_to,''), verb, COALESCE(source,''),
		       COALESCE(chat_name,'')
		  FROM messages WHERE id=?`, id).Scan(
		&m.ID, &m.ChatJID, &m.Sender, &m.Name, &m.Content, &ts,
		&fromMe, &botMsg, &m.ReplyToID, &m.Topic, &m.RoutedTo,
		&m.Verb, &m.Source, &m.ChatName,
	)
	if err != nil {
		return core.Message{}, false
	}
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, true
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) MessagesBefore(jid string, before time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if before.IsZero() {
		before = time.Now()
	}
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid = ? AND timestamp < ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		jid, core.TS(before), limit,
	)
	if err != nil {
		return nil, err
	}
	msgs, err := collectMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *Store) JIDRoutedToFolder(jid, folder string) bool {
	target := s.DefaultFolderForJID(jid)
	if target == "" {
		return false
	}
	return groupfolder.Contains(folder, target)
}

// JIDRoutableToFolder reports whether folder (or a descendant) is the target of
// any route matching jid when the verb predicate is ignored. Unlike
// JIDRoutedToFolder — which only sees the default verb="message" resolution —
// this recognizes a sub-folder that handles jid solely under a verb-scoped
// route (e.g. verb=mention), so a mention-only agent may reply to its own chat.
func (s *Store) JIDRoutableToFolder(jid, folder string) bool {
	msg := core.Message{ChatJID: jid}
	for _, r := range s.AllRoutes() {
		if !router.RouteMatchesIgnoreVerb(r, msg) {
			continue
		}
		t := core.ParseRouteTarget(r.Target).Folder
		if groupfolder.Contains(folder, t) {
			return true
		}
	}
	return false
}

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

// --- the archive lane (spec 5/8 "Message history") -----------------------
//
// The archive carries the WHOLE history, which no manifest does, so these
// four are messages' second reader and they live beside its first. The cursor
// derivation is here rather than with `chats` because it is the second half of
// ONE operation: an import that did not park the poller makes the agent answer
// every restored message.

// ArchiveMessageRow is the FULL row shape of routd.db's messages table —
// every column, unlike routd's own agent-facing read surface (msgReadCols,
// routd/reads.go) which omits reply_to_text/reply_to_sender/is_observed/
// errored because the agent never needs them. The archive is a full
// backup, not a filtered view, so it carries all of them verbatim (spec
// 5/8 "Message history": turn_id/chat_jid "carried verbatim").
type ArchiveMessageRow struct {
	ID            string `json:"id"`
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name,omitempty"`
	Content       string `json:"content"`
	Timestamp     string `json:"timestamp"`
	IsFromMe      bool   `json:"is_from_me"`
	IsBotMessage  bool   `json:"is_bot_message"`
	ForwardedFrom string `json:"forwarded_from,omitempty"`
	ReplyToID     string `json:"reply_to_id,omitempty"`
	ReplyToText   string `json:"reply_to_text,omitempty"`
	ReplyToSender string `json:"reply_to_sender,omitempty"`
	Topic         string `json:"topic,omitempty"`
	RoutedTo      string `json:"routed_to,omitempty"`
	Verb          string `json:"verb,omitempty"`
	Attachments   string `json:"attachments,omitempty"`
	Source        string `json:"source,omitempty"`
	IsObserved    bool   `json:"is_observed"`
	TurnID        string `json:"turn_id,omitempty"`
	Status        string `json:"status,omitempty"`
	PlatformID    string `json:"platform_id,omitempty"`
	ChatName      string `json:"chat_name,omitempty"`
	Errored       bool   `json:"errored"`
	LinkContext   string `json:"link_context,omitempty"`
}

// archiveMessageCols mirrors every column of routd/migrations/0001-initial-
// schema.sql's messages table plus 0006's errored and 0019's link_context —
// the full row, not routd/reads.go's msgReadCols agent-facing projection.
const archiveMessageCols = `id, chat_jid, sender, COALESCE(sender_name,''), content, timestamp,
	is_from_me, is_bot_message, COALESCE(forwarded_from,''), COALESCE(reply_to_id,''),
	COALESCE(reply_to_text,''), COALESCE(reply_to_sender,''), COALESCE(topic,''),
	COALESCE(routed_to,''), COALESCE(verb,''), COALESCE(attachments,''), COALESCE(source,''),
	is_observed, COALESCE(turn_id,''), COALESCE(status,''), COALESCE(platform_id,''),
	COALESCE(chat_name,''), errored, COALESCE(link_context,'')`

// ExportMessagesJSONL streams every messages row (one read tx — spec 5/8
// "Consistency levels") to w as JSONL (repo convention: .jl, one JSON
// object per line), ordered by rowid (insertion order) for deterministic
// output on an unchanged DB. Returns the row count.
func (s *Store) ExportMessagesJSONL(ctx context.Context, w io.Writer) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, fmt.Errorf("begin read tx: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+archiveMessageCols+" FROM messages ORDER BY rowid")
	if err != nil {
		return 0, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		var m ArchiveMessageRow
		var fromMe, botMsg, observed, errored int
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.SenderName, &m.Content, &m.Timestamp,
			&fromMe, &botMsg, &m.ForwardedFrom, &m.ReplyToID, &m.ReplyToText, &m.ReplyToSender,
			&m.Topic, &m.RoutedTo, &m.Verb, &m.Attachments, &m.Source, &observed,
			&m.TurnID, &m.Status, &m.PlatformID, &m.ChatName, &errored, &m.LinkContext); err != nil {
			return n, fmt.Errorf("scan message: %w", err)
		}
		m.IsFromMe = fromMe != 0
		m.IsBotMessage = botMsg != 0
		m.IsObserved = observed != 0
		m.Errored = errored != 0
		if err := enc.Encode(m); err != nil {
			return n, fmt.Errorf("encode message %s: %w", m.ID, err)
		}
		n++
	}
	return n, rows.Err()
}

// defaultImportBatch bounds how many messages ride one import transaction —
// a multi-hour transfer must not hold routd.db's write lock for its whole
// duration (spec 5/8 "Message history").
const defaultImportBatch = 500

// UnorderableMessage names one archived row this import refused: its
// timestamp is not an RFC3339 instant, so there is no core.TS rendering of it
// and it cannot be ordered against a cursor. The ID and the value as the
// archive carried it are both reported, because fixing the archive by hand is
// the operator's way back in.
type UnorderableMessage struct {
	ID        string
	Timestamp string
}

// fixedWidthTS re-renders an archived timestamp into core.TSLayout. Parsing
// with time.RFC3339Nano matches every reader of this column, and accepts each
// width an archive can carry — a whole second, a stripped fraction, a padded
// one — because they are all the same instant.
func fixedWidthTS(s string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", err
	}
	return core.TS(t), nil
}

// ImportMessagesJSONL reads JSONL from r and appends every row via INSERT OR
// IGNORE keyed on id (spec 5/8 "Message history": "idempotent bulk append,
// not rebuild" — re-running the same archive, or restoring onto a target
// that already has some of the same history, is a no-op on already-present
// rows). Runs in batches of batchSize rows per tx (<=0 uses
// defaultImportBatch). Returns the number of rows read, the set of chat_jids
// touched for the caller to derive agent_cursor from (DeriveAgentCursors,
// below — spec 5/8's Finding 4), and the rows it refused.
//
// Every timestamp is re-rendered through core.TS on the way in. An archive is
// the one place a foreign string reaches messages.timestamp, whose guarantee is
// that its string order is its time order, so this is where the value is
// brought into core.TSLayout rather than trusted (BUGS L219, L97).
//
// A row whose timestamp is not an RFC3339 instant is REFUSED — never written —
// and returned in refused for the caller to report. Per row, not per archive:
// a restore is a sequence of committed phases over a streaming tar, so failing
// here cannot roll back the config and secrets already applied, and the stream
// is unbounded and batched, so it cannot be checked up front either. Failing
// the whole restore would leave a half-restored instance while protecting
// nothing the per-row refusal does not — neither path writes the bad value.
// The import is idempotent, so correcting a named row and re-running completes
// the archive without duplicating what already landed.
func (s *Store) ImportMessagesJSONL(ctx context.Context, r io.Reader, batchSize int) (imported int, chatJIDs map[string]bool, refused []UnorderableMessage, err error) {
	if batchSize <= 0 {
		batchSize = defaultImportBatch
	}
	chatJIDs = map[string]bool{}
	dec := json.NewDecoder(r)
	var batch []ArchiveMessageRow
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, terr := s.db.BeginTx(ctx, nil)
		if terr != nil {
			return fmt.Errorf("begin import tx: %w", terr)
		}
		defer tx.Rollback()
		for _, m := range batch {
			if _, ierr := tx.ExecContext(ctx, `INSERT OR IGNORE INTO messages
				(id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
				 is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
				 topic, routed_to, verb, attachments, source, is_observed, turn_id, status,
				 platform_id, chat_name, errored, link_context)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				m.ID, m.ChatJID, m.Sender, archiveNullStr(m.SenderName), m.Content, m.Timestamp,
				archiveB2I(m.IsFromMe), archiveB2I(m.IsBotMessage), archiveNullStr(m.ForwardedFrom), archiveNullStr(m.ReplyToID),
				archiveNullStr(m.ReplyToText), archiveNullStr(m.ReplyToSender), m.Topic, m.RoutedTo, m.Verb,
				m.Attachments, m.Source, archiveB2I(m.IsObserved), archiveNullStr(m.TurnID), m.Status,
				archiveNullStr(m.PlatformID), m.ChatName, archiveB2I(m.Errored), archiveNullStr(m.LinkContext),
			); ierr != nil {
				return fmt.Errorf("insert message %s: %w", m.ID, ierr)
			}
			chatJIDs[m.ChatJID] = true
		}
		if cerr := tx.Commit(); cerr != nil {
			return fmt.Errorf("commit import batch: %w", cerr)
		}
		imported += len(batch)
		batch = batch[:0]
		return nil
	}
	for {
		var m ArchiveMessageRow
		derr := dec.Decode(&m)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			return imported, chatJIDs, refused, fmt.Errorf("decode message: %w", derr)
		}
		ts, terr := fixedWidthTS(m.Timestamp)
		if terr != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		m.Timestamp = ts
		batch = append(batch, m)
		if len(batch) >= batchSize {
			if ferr := flush(); ferr != nil {
				return imported, chatJIDs, refused, ferr
			}
		}
	}
	if ferr := flush(); ferr != nil {
		return imported, chatJIDs, refused, ferr
	}
	return imported, chatJIDs, refused, nil
}

// DeriveAgentCursors sets chats.agent_cursor = MAX(messages.timestamp) for every
// chat_jid in touched (spec 5/8 Finding 4). Without it, restoring message history
// makes routd's poller treat the whole import as unseen and dispatch a turn for
// every restored chat — the agent answering every historical message.
//
// Never moves an EXISTING cursor backward: a populated target's poller must not be
// walked back to re-open messages it already consumed.
func (s *Store) DeriveAgentCursors(ctx context.Context, touched map[string]bool) (int, error) {
	if len(touched) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cursor tx: %w", err)
	}
	defer tx.Rollback()
	jids := make([]string, 0, len(touched))
	for j := range touched {
		jids = append(jids, j)
	}
	sort.Strings(jids)
	n := 0
	for _, jid := range jids {
		res, uerr := tx.ExecContext(ctx, `INSERT INTO chats(jid, agent_cursor)
			SELECT ?, MAX(timestamp) FROM messages WHERE chat_jid = ?
			ON CONFLICT(jid) DO UPDATE SET agent_cursor = excluded.agent_cursor
			WHERE excluded.agent_cursor > COALESCE(chats.agent_cursor, '')`,
			jid, jid)
		if uerr != nil {
			return n, fmt.Errorf("derive agent_cursor for %s: %w", jid, uerr)
		}
		if aff, _ := res.RowsAffected(); aff > 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit cursor tx: %w", err)
	}
	return n, nil
}

// archiveB2I converts a bool to the 0/1 an INTEGER column stores.
func archiveB2I(b bool) int {
	if b {
		return 1
	}
	return 0
}

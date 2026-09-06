package store

import (
	"database/sql"
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
		m.Timestamp.Format(time.RFC3339Nano),
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
			since.Format(time.RFC3339Nano), botName+"%",
		)
	} else {
		args := make([]any, 0, len(jids)+2)
		for _, jid := range jids {
			args = append(args, jid)
		}
		args = append(args, since.Format(time.RFC3339Nano), botName+"%")
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
		jid, since.Format(time.RFC3339Nano), botName+"%",
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
		jid, since.Format(time.RFC3339Nano),
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
		jid, topic, before.Format(time.RFC3339Nano), limit,
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
		jid, topic, after.Format(time.RFC3339Nano), limit,
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
		engaged = until.UTC().Format(time.RFC3339Nano)
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
		jid, before.Format(time.RFC3339Nano), limit,
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

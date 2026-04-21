package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) PutMessage(m core.Message) error {
	if m.Topic == "" {
		m.Topic = s.GetStickyTopic(m.ChatJID)
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message, forwarded_from,
		  reply_to_id, reply_to_text, reply_to_sender, topic, routed_to, verb, attachments, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content,
		m.Timestamp.Format(time.RFC3339Nano),
		btoi(m.FromMe), btoi(m.BotMsg),
		nilIfEmpty(m.ForwardedFrom),
		nilIfEmpty(m.ReplyToID), nilIfEmpty(m.ReplyToText), nilIfEmpty(m.ReplyToSender),
		m.Topic, m.RoutedTo, m.Verb, m.Attachments, m.Source,
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
	topic, routed_to, verb, attachments, source, errored`

// NewMessages returns new inbound messages since `since`. If jids is empty,
// no chat_jid filter is applied (all new messages from all chats).
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

func (s *Store) HasPendingMessages(jid, botName string) bool {
	cursor := s.GetAgentCursor(jid)
	var n int
	s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM messages
		 WHERE chat_jid = ? AND timestamp > ? AND is_bot_message = 0
		   AND sender NOT LIKE ?)`,
		jid, cursor.Format(time.RFC3339Nano), botName+"%",
	).Scan(&n)
	return n == 1
}

// MarkMessagesErrored annotates the given message IDs so the next agent
// run sees them tagged "errored" and can try a different approach. The
// messages stay visible to all read paths; only the circuit breaker
// prunes them via DeleteErroredMessages.
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

// DeleteErroredMessages hard-prunes all errored rows for a chat.
// Called by the circuit breaker hard-reset path.
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
		&m.Topic, &m.RoutedTo, &m.Verb, &m.Attachments, &m.Source, &errored); err != nil {
		return m, err
	}
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Errored = errored != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, nil
}

// LatestSource returns the source (adapter name) of the most recent
// inbound message in chat jid. Empty string if none recorded.
func (s *Store) LatestSource(jid string) string {
	var src string
	s.db.QueryRow(
		`SELECT source FROM messages
		 WHERE chat_jid = ? AND source != '' AND is_bot_message = 0
		 ORDER BY timestamp DESC LIMIT 1`,
		jid,
	).Scan(&src)
	return src
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
	jid := "web:" + folder
	if limit <= 0 || limit > 100 {
		limit = 50
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

func (s *Store) MessagesSinceTopic(folder, topic string, after time.Time, limit int) ([]core.Message, error) {
	jid := "web:" + folder
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

// ObservedMessagesSince returns recent inbound messages from chats that
// route to groupFolder via the routes table, excluding excludeJid. Used
// for "what other chats has this group seen lately" prompts.
func (s *Store) ObservedMessagesSince(groupFolder, excludeJid, since string) []core.Message {
	jids := s.RouteSourceJIDsInWorld(groupFolder)
	if len(jids) == 0 {
		return nil
	}
	args := make([]any, 0, len(jids)+2)
	for _, jid := range jids {
		if jid != excludeJid {
			args = append(args, jid)
		}
	}
	if len(args) == 0 {
		return nil
	}
	args = append(args, since)
	ph := "(" + strings.TrimSuffix(strings.Repeat("?,", len(args)-1), ",") + ")"
	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid IN `+ph+` AND timestamp > ?
		   AND is_bot_message = 0 AND content != ''
		 ORDER BY timestamp ASC
		 LIMIT 100`,
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			continue
		}
		out = append(out, m)
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

func (s *Store) SetLastReplyID(jid, topic, replyID string) {
	s.db.Exec(
		`INSERT INTO chat_reply_state (jid, topic, last_reply_id) VALUES (?,?,?)
		 ON CONFLICT(jid, topic) DO UPDATE SET last_reply_id=excluded.last_reply_id`,
		jid, topic, replyID,
	)
}

func (s *Store) RoutedToByMessageID(id string) string {
	var routedTo string
	s.db.QueryRow(`SELECT COALESCE(routed_to,'') FROM messages WHERE id=? AND routed_to!=''`,
		id).Scan(&routedTo)
	return routedTo
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
		 WHERE chat_jid = ? AND timestamp < ? AND is_bot_message = 0
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

// JIDRoutedToFolder reports whether jid resolves (via the routes table)
// to folder or any descendant of folder.
func (s *Store) JIDRoutedToFolder(jid, folder string) bool {
	target := s.DefaultFolderForJID(jid)
	if target == "" {
		return false
	}
	return target == folder || strings.HasPrefix(target, folder+"/")
}

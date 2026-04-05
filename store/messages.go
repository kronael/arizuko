package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) PutMessage(m core.Message) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message, forwarded_from,
		  reply_to_id, reply_to_text, reply_to_sender, topic, routed_to, verb, attachments)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content,
		m.Timestamp.Format(time.RFC3339Nano),
		btoi(m.FromMe), btoi(m.BotMsg),
		nilIfEmpty(m.ForwardedFrom),
		nilIfEmpty(m.ReplyToID), nilIfEmpty(m.ReplyToText), nilIfEmpty(m.ReplyToSender),
		m.Topic, m.RoutedTo, m.Verb, m.Attachments,
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
	topic, routed_to, verb, attachments`

func (s *Store) NewMessages(jids []string, since time.Time, botName string) ([]core.Message, time.Time, error) {
	if len(jids) == 0 {
		return nil, since, nil
	}
	args := make([]any, 0, len(jids)+2)
	for _, jid := range jids {
		args = append(args, jid)
	}
	args = append(args, since.Format(time.RFC3339Nano), botName+"%")
	ph := "(" + strings.TrimSuffix(strings.Repeat("?,", len(jids)), ",") + ")"

	rows, err := s.db.Query(
		`SELECT `+msgCols+` FROM messages
		 WHERE chat_jid IN `+ph+`
		   AND timestamp > ?
		   AND is_bot_message = 0
		   AND sender NOT LIKE ?
		 ORDER BY timestamp ASC
		 LIMIT 200`,
		args...,
	)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()

	var msgs []core.Message
	var hi time.Time
	for rows.Next() {
		m, ts, err := scanMessage(rows)
		if err != nil {
			return nil, since, err
		}
		msgs = append(msgs, m)
		if ts.After(hi) {
			hi = ts
		}
	}
	if hi.IsZero() {
		hi = since
	}
	return msgs, hi, rows.Err()
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
		m, _, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func scanMessage(r rowScanner) (core.Message, time.Time, error) {
	var m core.Message
	var ts string
	var fromMe, botMsg int
	if err := r.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Name, &m.Content,
		&ts, &fromMe, &botMsg, &m.ForwardedFrom,
		&m.ReplyToID, &m.ReplyToText, &m.ReplyToSender,
		&m.Topic, &m.RoutedTo, &m.Verb, &m.Attachments); err != nil {
		return m, time.Time{}, err
	}
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, m.Timestamp, nil
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

func (s *Store) ObservedMessagesSince(groupFolder, excludeJid, since string) []core.Message {
	rows, err := s.db.Query(
		`SELECT DISTINCT `+msgCols+` FROM messages
		 JOIN routes r ON (r.jid = messages.chat_jid OR r.jid = substr(messages.chat_jid, 1, instr(messages.chat_jid, ':')))
		 WHERE r.target = ? AND messages.chat_jid != ? AND messages.timestamp > ?
		   AND messages.is_bot_message = 0 AND messages.content != '' AND messages.content IS NOT NULL
		 ORDER BY messages.timestamp ASC
		 LIMIT 100`,
		groupFolder, excludeJid, since,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		m, _, err := scanMessage(rows)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (s *Store) ActiveWebJIDs(since time.Time) []string {
	rows, err := s.db.Query(
		`SELECT DISTINCT chat_jid FROM messages
		 WHERE chat_jid LIKE 'web:%' AND timestamp > ?`,
		since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		jids = append(jids, jid)
	}
	return jids
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

func (s *Store) StoreOutbound(entry core.OutboundEntry) error {
	id := "out-" + entry.PlatformMsgID
	if entry.PlatformMsgID == "" {
		id = fmt.Sprintf("out-unsent-%d", time.Now().UnixNano())
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message,
		  reply_to_id, source, group_folder)
		 VALUES (?, ?, 'bot', ?, ?, 1, 1, ?, ?, ?)`,
		id, entry.ChatJID, entry.Content, time.Now().Format(time.RFC3339Nano),
		nilIfEmpty(entry.ReplyToID), nilIfEmpty(entry.Source), nilIfEmpty(entry.GroupFolder),
	)
	return err
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

func (s *Store) JIDRoutedToFolder(jid, folder string) bool {
	var count int
	s.db.QueryRow(
		`SELECT COUNT(*) FROM routes WHERE jid = ? AND (target = ? OR target LIKE ?)`,
		jid, folder, folder+"/%",
	).Scan(&count)
	return count > 0
}

package store

import (
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) PutMessage(m core.Message) error {
	// INSERT OR IGNORE: same message ID from same platform is idempotent.
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message, forwarded_from,
		  reply_to_id, reply_to_text, reply_to_sender, topic)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content,
		m.Timestamp.Format(time.RFC3339Nano),
		btoi(m.FromMe), btoi(m.BotMsg),
		nilIfEmpty(m.ForwardedFrom),
		nilIfEmpty(m.ReplyToID), nilIfEmpty(m.ReplyToText), nilIfEmpty(m.ReplyToSender),
		m.Topic,
	)
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Store) AppendContent(id, suffix string) error {
	_, err := s.db.Exec(
		`UPDATE messages SET content = content || ? WHERE id = ?`,
		suffix, id,
	)
	return err
}

func (s *Store) NewMessages(jids []string, since time.Time, botName string) ([]core.Message, time.Time, error) {
	if len(jids) == 0 {
		return nil, since, nil
	}
	ph := "("
	args := make([]any, 0, len(jids)+2)
	for i, jid := range jids {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args = append(args, jid)
	}
	ph += ")"
	sinceStr := since.Format(time.RFC3339Nano)
	args = append(args, sinceStr, botName+"%")

	rows, err := s.db.Query(
		`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		        is_from_me, is_bot_message, forwarded_from,
		        reply_to_id, reply_to_text, reply_to_sender, topic
		 FROM messages
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
		m, ts := scanMessage(rows)
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
		`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		        is_from_me, is_bot_message, forwarded_from,
		        reply_to_id, reply_to_text, reply_to_sender, topic
		 FROM messages
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
	defer rows.Close()

	var msgs []core.Message
	for rows.Next() {
		m, _ := scanMessage(rows)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(r rowScanner) (core.Message, time.Time) {
	var m core.Message
	var ts string
	var fromMe, botMsg int
	var name, fwdFrom, replyID, replyText, replySender, topic *string
	r.Scan(&m.ID, &m.ChatJID, &m.Sender, &name, &m.Content,
		&ts, &fromMe, &botMsg, &fwdFrom, &replyID, &replyText, &replySender, &topic)
	if name != nil {
		m.Name = *name
	}
	if fwdFrom != nil {
		m.ForwardedFrom = *fwdFrom
	}
	if replyID != nil {
		m.ReplyToID = *replyID
	}
	if replyText != nil {
		m.ReplyToText = *replyText
	}
	if replySender != nil {
		m.ReplyToSender = *replySender
	}
	if topic != nil {
		m.Topic = *topic
	}
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, m.Timestamp
}

// TopicSummary is a topic with its last message time and preview.
type TopicSummary struct {
	ID           string
	Preview      string
	LastAt       time.Time
	MessageCount int
}

// Topics returns all topics for a group folder, newest first.
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

// MessagesByTopic returns messages for a group/topic cursor-paginated, newest first.
func (s *Store) MessagesByTopic(folder, topic string, before time.Time, limit int) ([]core.Message, error) {
	jid := "web:" + folder
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		        is_from_me, is_bot_message, forwarded_from,
		        reply_to_id, reply_to_text, reply_to_sender, topic
		 FROM messages
		 WHERE chat_jid = ? AND topic = ? AND timestamp < ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		jid, topic, before.Format(time.RFC3339Nano), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []core.Message
	for rows.Next() {
		m, _ := scanMessage(rows)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GroupBySlinkToken finds a group by its slink token.
func (s *Store) GroupBySlinkToken(token string) (core.Group, bool) {
	for _, g := range s.AllGroups() {
		if g.SlinkToken == token {
			return g, true
		}
	}
	return core.Group{}, false
}

// GroupByFolder finds a group by its folder path.
func (s *Store) GroupByFolder(folder string) (core.Group, bool) {
	for _, g := range s.AllGroups() {
		if g.Folder == folder {
			return g, true
		}
	}
	return core.Group{}, false
}

// TopicByMessageID returns the topic of a message by its ID and chat JID.
func (s *Store) TopicByMessageID(id, jid string) string {
	var topic string
	s.db.QueryRow(`SELECT COALESCE(topic,'') FROM messages WHERE id=? AND chat_jid=?`,
		id, jid).Scan(&topic)
	return topic
}

// MessageTimestampByID returns the timestamp of a message by its ID and chat JID.
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

// MessagesSinceTopic returns messages for a group/topic after a given time, oldest first.
func (s *Store) MessagesSinceTopic(folder, topic string, after time.Time, limit int) ([]core.Message, error) {
	jid := "web:" + folder
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		        is_from_me, is_bot_message, forwarded_from,
		        reply_to_id, reply_to_text, reply_to_sender, topic
		 FROM messages
		 WHERE chat_jid = ? AND topic = ? AND timestamp > ?
		 ORDER BY timestamp ASC
		 LIMIT ?`,
		jid, topic, after.Format(time.RFC3339Nano), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []core.Message
	for rows.Next() {
		m, _ := scanMessage(rows)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ActiveWebJIDs returns distinct web: JIDs that have messages since `since`.
// Used by the gateway poll loop to discover web chat conversations.
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

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

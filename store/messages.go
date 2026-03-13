package store

import (
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) PutMessage(m core.Message) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages
		 (id, chat_jid, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message, forwarded_from,
		  reply_to_id, reply_to_text, reply_to_sender)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content,
		m.Timestamp.Format(time.RFC3339Nano),
		btoi(m.FromMe), btoi(m.BotMsg),
		nilIfEmpty(m.ForwardedFrom),
		nilIfEmpty(m.ReplyToID), nilIfEmpty(m.ReplyToText), nilIfEmpty(m.ReplyToSender),
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
		        reply_to_id, reply_to_text, reply_to_sender
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
		        reply_to_id, reply_to_text, reply_to_sender
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
	var name, fwdFrom, replyID, replyText, replySender *string
	r.Scan(&m.ID, &m.ChatJID, &m.Sender, &name, &m.Content,
		&ts, &fromMe, &botMsg, &fwdFrom, &replyID, &replyText, &replySender)
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
	m.FromMe = fromMe != 0
	m.BotMsg = botMsg != 0
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m, m.Timestamp
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

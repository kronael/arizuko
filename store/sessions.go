package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) GetSession(folder, topic string) (string, bool) {
	var id string
	err := s.db.QueryRow(
		`SELECT session_id FROM sessions WHERE group_folder = ? AND topic = ?`,
		folder, topic,
	).Scan(&id)
	return id, err == nil
}

func (s *Store) SetSession(folder, topic, id string) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (group_folder, topic, session_id) VALUES (?, ?, ?)
		 ON CONFLICT(group_folder, topic) DO UPDATE SET session_id = excluded.session_id`,
		folder, topic, id,
	)
	return err
}

func (s *Store) DeleteSession(folder, topic string) error {
	_, err := s.db.Exec(
		`DELETE FROM sessions WHERE group_folder = ? AND topic = ?`,
		folder, topic,
	)
	return err
}

func (s *Store) AllSessions() map[string]string {
	rows, err := s.db.Query(
		`SELECT group_folder, session_id FROM sessions WHERE topic = ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var folder, id string
		rows.Scan(&folder, &id)
		out[folder] = id
	}
	return out
}

func (s *Store) GetState(key string) string {
	var val string
	s.db.QueryRow(`SELECT value FROM router_state WHERE key = ?`, key).Scan(&val)
	return val
}

func (s *Store) SetState(key, val string) error {
	_, err := s.db.Exec(
		`INSERT INTO router_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, val,
	)
	return err
}

func (s *Store) EnqueueSysMsg(folder, origin, event, body string) error {
	_, err := s.db.Exec(
		`INSERT INTO system_messages (group_id, origin, event, body, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		folder, origin, event, body, time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) FlushSysMsgs(folder string) string {
	rows, err := s.db.Query(
		`SELECT id, origin, event, body FROM system_messages
		 WHERE group_id = ? ORDER BY id ASC`, folder)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var b strings.Builder
	var ids []int64
	for rows.Next() {
		var id int64
		var origin, event, body string
		rows.Scan(&id, &origin, &event, &body)
		ids = append(ids, id)
		fmt.Fprintf(&b, "<system_message origin=%q event=%q>%s</system_message>\n", origin, event, body)
	}

	if len(ids) > 0 {
		ph := strings.Repeat("?,", len(ids))
		ph = ph[:len(ph)-1]
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		s.db.Exec(`DELETE FROM system_messages WHERE id IN (`+ph+`)`, args...)
	}

	return b.String()
}

func (s *Store) RecordSession(folder, sessionID string) (int64, error) {
	r, err := s.db.Exec(
		`INSERT INTO session_log (group_folder, session_id, started_at)
		 VALUES (?, ?, ?)`,
		folder, sessionID, time.Now().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) EndSession(rowID int64, result, errStr string, msgs int) error {
	_, err := s.db.Exec(
		`UPDATE session_log SET ended_at = ?, result = ?, error = ?, message_count = ?
		 WHERE id = ?`,
		time.Now().Format(time.RFC3339), result, errStr, msgs, rowID,
	)
	return err
}

func (s *Store) RecentSessions(folder string, n int) []core.SessionRecord {
	rows, err := s.db.Query(
		`SELECT id, group_folder, session_id, started_at, ended_at,
		        result, error, message_count
		 FROM session_log
		 WHERE group_folder = ?
		 ORDER BY id DESC LIMIT ?`, folder, n)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []core.SessionRecord
	for rows.Next() {
		var sr core.SessionRecord
		var startedAt string
		var endedAt, result, errStr *string
		var msgCount *int
		rows.Scan(&sr.ID, &sr.Folder, &sr.SessionID, &startedAt,
			&endedAt, &result, &errStr, &msgCount)
		sr.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		if endedAt != nil {
			t, _ := time.Parse(time.RFC3339, *endedAt)
			sr.EndedAt = &t
		}
		if result != nil {
			sr.Result = *result
		}
		if errStr != nil {
			sr.Error = *errStr
		}
		if msgCount != nil {
			sr.MsgCount = *msgCount
		}
		out = append(out, sr)
	}
	return out
}

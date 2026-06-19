package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
)

func (s *Store) GetSession(folder, topic string) (string, bool) {
	var id string
	err := s.db.QueryRow(
		`SELECT session_id FROM sessions WHERE group_folder = ? AND topic = ?`,
		folder, topic,
	).Scan(&id)
	return id, err == nil
}

// SetSession upserts a session_id for (folder, topic). Lineage
// columns are owned by EnsureTopicLineage / ForkTopic (spec 6/F),
// which always run before the first agent turn — so this UPSERT
// only ever flips session_id on an existing row, never seeds lineage.
func (s *Store) SetSession(folder, topic, id string) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (group_folder, topic, session_id) VALUES (?, ?, ?)
		 ON CONFLICT(group_folder, topic) DO UPDATE SET session_id = excluded.session_id`,
		folder, topic, id,
	)
	return err
}

// EnsureTopicLineage inserts a sessions row for (folder, topic) with
// lineage if no row exists yet. Idempotent: a no-op when the row
// already exists. parentTopic="" means fork from main (the default for
// any non-main topic). Caller passes main topic "" untouched — main
// has no parent, so we skip. newSessionID is used only on INSERT.
// Returns inserted=true when a new row was created (caller should
// trigger the cp of the parent session file — spec 6/F).
func (s *Store) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	if topic == "" {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions
		   (group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		folder, topic, newSessionID, parentTopic, now, now,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) DeleteSession(folder, topic string) error {
	_, err := s.db.Exec(
		`DELETE FROM sessions WHERE group_folder = ? AND topic = ?`,
		folder, topic,
	)
	return err
}

// TopicLineage returns the lineage row for (folder, topic). After
// spec 6/F rev6 the prompt path only consumes ObservedCursor; parent
// + forked_at remain as audit metadata. ok=false when no row exists.
func (s *Store) TopicLineage(folder, topic string) (core.TopicLineage, bool) {
	var parent, forked, cursor *string
	err := s.db.QueryRow(
		`SELECT parent_topic, forked_at, observed_cursor
		 FROM sessions WHERE group_folder = ? AND topic = ?`,
		folder, topic,
	).Scan(&parent, &forked, &cursor)
	if err != nil {
		return core.TopicLineage{}, false
	}
	out := core.TopicLineage{Folder: folder, Topic: topic, ParentTopic: parent}
	if forked != nil {
		out.ForkedAt = *forked
	}
	if cursor != nil {
		out.ObservedCursor = *cursor
	}
	return out, true
}

// UpdateObservedCursor advances a topic's observed cursor to ts
// (RFC3339Nano UTC). Idempotent: only writes when the new cursor is
// strictly greater than the stored one (or stored is NULL).
func (s *Store) UpdateObservedCursor(folder, topic, ts string) error {
	_, err := s.db.Exec(
		`UPDATE sessions
		 SET observed_cursor = ?
		 WHERE group_folder = ? AND topic = ?
		   AND (observed_cursor IS NULL OR observed_cursor < ?)`,
		ts, folder, topic, ts,
	)
	return err
}

// ForkTopic creates a new sessions row as a fork of (folder, parent).
// Returns ErrTopicExists if child already exists and force=false.
// On success the child carries a fresh session_id and
// parent_topic=parent, forked_at=now, observed_cursor=now.
func (s *Store) ForkTopic(folder, parent, child, newSessionID string, force bool) error {
	if child == "" {
		return fmt.Errorf("fork: child topic empty")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if force {
		_, err := s.db.Exec(
			`INSERT INTO sessions (group_folder, topic, session_id,
			                       parent_topic, forked_at, observed_cursor)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(group_folder, topic) DO UPDATE SET
			   session_id      = excluded.session_id,
			   parent_topic    = excluded.parent_topic,
			   forked_at       = excluded.forked_at,
			   observed_cursor = excluded.observed_cursor`,
			folder, child, newSessionID, parent, now, now,
		)
		return err
	}
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions
		   (group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		folder, child, newSessionID, parent, now, now,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.ErrTopicExists
	}
	return nil
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
	tx, err := s.db.Begin()
	if err != nil {
		return ""
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT origin, event, body FROM system_messages
		 WHERE group_id = ? ORDER BY id ASC`, folder)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for rows.Next() {
		var origin, event, body string
		if err := rows.Scan(&origin, &event, &body); err != nil {
			rows.Close()
			return ""
		}
		fmt.Fprintf(&b, "<system origin=%q event=%q>%s</system>\n", origin, event, body)
	}
	scanErr := rows.Err()
	rows.Close()
	// Never delete rows we failed to render: leave them for the next flush.
	if scanErr != nil {
		return ""
	}

	if b.Len() == 0 {
		return ""
	}
	if _, err := tx.Exec(`DELETE FROM system_messages WHERE group_id = ?`, folder); err != nil {
		return ""
	}
	if err := tx.Commit(); err != nil {
		return ""
	}
	return b.String()
}

func (s *Store) RecordSession(folder, sessionID string, startedAt time.Time) (int64, error) {
	r, err := s.db.Exec(
		`INSERT INTO session_log (group_folder, session_id, started_at)
		 VALUES (?, ?, ?)`,
		folder, sessionID, startedAt.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) EndSession(rowID int64, sessionID, result, errStr string, msgs int) error {
	_, err := s.db.Exec(
		`UPDATE session_log SET ended_at = ?,
		        session_id = COALESCE(NULLIF(?, ''), session_id),
		        result = ?, error = ?, message_count = ?
		 WHERE id = ?`,
		time.Now().Format(time.RFC3339), sessionID, result, errStr, msgs, rowID,
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

package store

// session_log.go holds every read and write of `session_log`: one row per agent
// run, opened when the run starts and closed when it exits.
//
// It is HISTORY and it is still a table of columns. `sessions` — the topic
// continuity beside it — is an entity now, and these two were in one file only
// because their names rhyme: one is mutable state keyed by (folder, topic), the
// other is an append-and-close log keyed by a sequence the engine mints. Spec
// 6/37 rule 5 is that separating them is the point, and rule 1 is that one file
// holds one entity.

import (
	"time"

	"github.com/kronael/arizuko/core"
)

func (s *Store) RecordSession(folder, sessionID string, startedAt time.Time) (int64, error) {
	r, err := s.db.Exec(
		`INSERT INTO session_log (group_folder, session_id, started_at)
		 VALUES (?, ?, ?)`,
		folder, sessionID, startedAt.Format(time.RFC3339))
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
		time.Now().Format(time.RFC3339), sessionID, result, errStr, msgs, rowID)
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

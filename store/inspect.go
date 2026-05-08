package store

import "time"

type ErroredChat struct {
	ChatJID  string    `json:"chat_jid"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	RoutedTo string    `json:"routed_to"`
}

func (s *Store) ErroredChats(folder string, isRoot bool) []ErroredChat {
	scope := folder
	if isRoot {
		scope = ""
	}
	rows, err := s.db.Query(
		`SELECT chat_jid, COUNT(*), MAX(timestamp), COALESCE(MAX(routed_to),'')
		 FROM messages WHERE errored = 1
		   AND (? = '' OR routed_to = ? OR routed_to LIKE ?||'/%')
		 GROUP BY chat_jid ORDER BY MAX(timestamp) DESC`,
		scope, scope, scope)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ErroredChat
	for rows.Next() {
		var e ErroredChat
		var ts string
		if err := rows.Scan(&e.ChatJID, &e.Count, &ts, &e.RoutedTo); err != nil {
			continue
		}
		e.LastAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out
}

// TaskRunLog is a single row from task_run_logs, surfaced to operators.
type TaskRunLog struct {
	ID         int64     `json:"id"`
	TaskID     string    `json:"task_id"`
	RunAt      time.Time `json:"run_at"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func (s *Store) TaskRunLogs(taskID string, limit int) []TaskRunLog {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, task_id, run_at, COALESCE(duration_ms,0), status,
		        COALESCE(result,''), COALESCE(error,'')
		 FROM task_run_logs WHERE task_id = ?
		 ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TaskRunLog
	for rows.Next() {
		var l TaskRunLog
		var runAt string
		if err := rows.Scan(&l.ID, &l.TaskID, &runAt, &l.DurationMS,
			&l.Status, &l.Result, &l.Error); err != nil {
			continue
		}
		l.RunAt, _ = time.Parse(time.RFC3339, runAt)
		out = append(out, l)
	}
	return out
}

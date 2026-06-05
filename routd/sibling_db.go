package routd

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/core"
)

// sibling_db.go gives routd READ-ONLY access to the sibling DBs that share
// store/ but are owned (written) by other daemons in the split topology:
//
//   - messages.db — timed writes scheduled_tasks; slakd writes pane_sessions.
//   - runed.db    — runed writes session_log (per-spawn history).
//
// routd reads them to reach gated's full prompt/spawn context (tasks.json
// snapshot, Slack-pane hints, previous-session continuity). Ownership stays
// with the writers; routd never mutates these tables. A missing file leaves
// the handle nil and every accessor returns the empty result — same shape as
// gated against an empty store, no hard dependency on the sibling daemon.

// openSiblings opens read-only handles to the sibling DBs in dir, if present.
// Absent file → nil handle (the owning daemon may not run in this deployment).
func openSiblings(dir string) (msgs, runed *sql.DB) {
	return openRO(filepath.Join(dir, "messages.db")), openRO(filepath.Join(dir, "runed.db"))
}

// openRO opens path read-only. Returns nil when the file is absent or the open
// fails — callers treat nil as "no data", never an error.
func openRO(path string) *sql.DB {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil
	}
	return db
}

// SiblingTasks reads scheduled_tasks from messages.db (timed's table) for the
// tasks.json spawn snapshot. Port of store.ListTasks: a root group sees every
// task (owner filter empty); a child sees only its own. nil handle → nil.
func (d *DB) SiblingTasks(folder string, isRoot bool) []core.Task {
	if d.msgs == nil {
		return nil
	}
	owner := folder
	if isRoot {
		owner = ""
	}
	rows, err := d.msgs.Query(
		`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode
		 FROM scheduled_tasks WHERE (? = '' OR owner = ?) ORDER BY created_at DESC`, owner, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		var t core.Task
		var cron, nextRun *string
		var created string
		if err := rows.Scan(&t.ID, &t.Owner, &t.ChatJID, &t.Prompt,
			&cron, &nextRun, &t.Status, &created, &t.ContextMode); err != nil {
			return out
		}
		t.Created, _ = time.Parse(time.RFC3339, created)
		if cron != nil {
			t.Cron = *cron
		}
		if nextRun != nil {
			v, _ := time.Parse(time.RFC3339, *nextRun)
			t.NextRun = &v
		}
		if t.ContextMode == "" {
			t.ContextMode = "group"
		}
		out = append(out, t)
	}
	return out
}

// SiblingPaneContextJID reads pane_sessions from messages.db (slakd's table)
// and returns (contextJID, true) when a Slack assistant pane exists for the DM
// channelID. Port of store.GetPaneByChannel, narrowed to the field paneHints
// needs. nil handle / no row → ("", false).
func (d *DB) SiblingPaneContextJID(channelID string) (string, bool) {
	if d.msgs == nil {
		return "", false
	}
	var ctx sql.NullString
	err := d.msgs.QueryRow(
		`SELECT context_jid FROM pane_sessions WHERE channel_id = ?
		 ORDER BY opened_at DESC LIMIT 1`, channelID).Scan(&ctx)
	if err != nil {
		return "", false
	}
	return ctx.String, true
}

// SiblingRecentSessions reads the n most recent session_log rows from runed.db
// (runed's table) for the new_session continuity hint. Port of
// store.RecentSessions. nil handle → nil.
func (d *DB) SiblingRecentSessions(folder string, n int) []core.SessionRecord {
	if d.runedDB == nil {
		return nil
	}
	rows, err := d.runedDB.Query(
		`SELECT id, group_folder, session_id, started_at, ended_at,
		        result, error, message_count
		 FROM session_log WHERE group_folder = ? ORDER BY id DESC LIMIT ?`, folder, n)
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
		if err := rows.Scan(&sr.ID, &sr.Folder, &sr.SessionID, &startedAt,
			&endedAt, &result, &errStr, &msgCount); err != nil {
			return out
		}
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

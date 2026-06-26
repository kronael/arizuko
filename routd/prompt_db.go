package routd

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// prompt_db.go holds the routd.DB query methods that back buildAgentPrompt: the
// system-message flush, the per-topic observed-context window, topic lineage, and
// the group's observe-window override.

// FlushSysMsgs renders queued system_messages for folder as <system> lines and
// deletes them in the same tx (at-most-once delivery). Columns (source, kind,
// body) render as (origin, event, body). Rows that fail to render are left for
// the next flush.
func (d *DB) FlushSysMsgs(folder string) string {
	tx, err := d.db.Begin()
	if err != nil {
		return ""
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT source, kind, body FROM system_messages WHERE folder = ? ORDER BY id ASC`, folder)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for rows.Next() {
		var source, kind, body string
		if err := rows.Scan(&source, &kind, &body); err != nil {
			rows.Close()
			return ""
		}
		fmt.Fprintf(&b, "<system origin=%q event=%q>%s</system>\n", source, kind, body)
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
	if _, err := tx.Exec(`DELETE FROM system_messages WHERE folder = ?`, folder); err != nil {
		return ""
	}
	if err := tx.Commit(); err != nil {
		return ""
	}
	return b.String()
}

// EnqueueSysMsg appends one system_messages row for folder. emitSystemEvents is
// the producer; FlushSysMsgs (above) is the consumer.
func (d *DB) EnqueueSysMsg(folder, source, kind, body string) error {
	_, err := d.db.Exec(
		`INSERT INTO system_messages (folder, source, kind, body, created)
		 VALUES (?, ?, ?, ?, ?)`,
		folder, source, kind, body, nowTS())
	return err
}

// TopicLineage returns the lineage row for (folder, topic); the prompt path
// consumes only ObservedCursor (parent + forked_at are audit metadata).
// ok=false when no row exists.
func (d *DB) TopicLineage(folder, topic string) (core.TopicLineage, bool) {
	var parent, forked, cursor *string
	err := d.db.QueryRow(
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

// UpdateObservedCursor advances a topic's observed cursor to ts (RFC3339Nano
// UTC), monotonically (only when the new value is strictly greater, or NULL).
// UPSERT, not UPDATE: buildAgentPrompt advances the cursor BEFORE PutSession
// creates the (folder,topic) row, so a plain UPDATE would match zero rows on a
// topic's first turns and re-include the observed window. The inserted row
// carries session_id='' (PutSession fills it post-run without clobbering
// observed_cursor).
func (d *DB) UpdateObservedCursor(folder, topic, ts string) error {
	_, err := d.db.Exec(
		`INSERT INTO sessions(group_folder, topic, session_id, observed_cursor)
		 VALUES(?,?,'',?)
		 ON CONFLICT(group_folder, topic) DO UPDATE SET observed_cursor = excluded.observed_cursor
		   WHERE sessions.observed_cursor IS NULL OR sessions.observed_cursor < excluded.observed_cursor`,
		folder, topic, ts,
	)
	return err
}

// GroupObserveWindow returns the group's stored observe-window override as
// (messages, chars). A NULL key yields -1 so the cfg default wins (only a
// >= 0 value overrides). (-1,-1) when the group has no row.
func (d *DB) GroupObserveWindow(folder string) (int, int) {
	return store.New(d.db).GroupObserveWindow(folder)
}

// WatchedSources returns the source folders folder watches (group_watchers
// rows where observer=folder) — the observe_group ambient join. Empty slice
// when folder watches nothing.
func (d *DB) WatchedSources(folder string) []string {
	rows, err := d.db.Query(`SELECT source FROM group_watchers WHERE observer = ?`, folder)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

// ObservedSince returns the trailing window of observed messages for folder
// whose timestamp is strictly greater than cursor (RFC3339Nano UTC). Empty
// cursor means "no lower bound; the window cap decides". Capped by maxMsgs and
// maxChars; the oldest messages drop first when the char cap binds.
//
// Two shapes UNION'd: (a) is_observed=1 rows routed to folder; (b) is_observed=0
// primary-delivery rows routed to folder's watched sources (observe_group
// ambient context).
func (d *DB) ObservedSince(folder, cursor string, maxMsgs, maxChars int) []core.Message {
	if maxMsgs <= 0 || maxChars <= 0 {
		return nil
	}
	watched := d.WatchedSources(folder)

	args := []any{folder}
	cursorClause := ""
	if cursor != "" {
		cursorClause = " AND timestamp > ?"
	}

	var q string
	if len(watched) > 0 {
		watchedPH := "?" + strings.Repeat(",?", len(watched)-1)
		// Arg order matches placeholder order: folder, first cursor, watched
		// folders, second cursor, LIMIT.
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
		q = `SELECT ` + msgReadCols + ` FROM messages
		     WHERE routed_to = ? AND is_observed = 1
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     UNION ALL
		     SELECT ` + msgReadCols + ` FROM messages
		     WHERE routed_to IN (` + watchedPH + `) AND is_observed = 0
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     ORDER BY timestamp DESC
		     LIMIT ?`
	} else {
		if cursor != "" {
			args = append(args, cursor)
		}
		args = append(args, maxMsgs)
		q = `SELECT ` + msgReadCols + ` FROM messages
		     WHERE routed_to = ? AND is_observed = 1
		       AND is_bot_message = 0 AND content != ''` + cursorClause + `
		     ORDER BY timestamp DESC
		     LIMIT ?`
	}
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	// scanMessages drains newest-first into rev; take the youngest whose
	// cumulative chars fit under maxChars, then reverse to chronological.
	rev, _, err := scanMessages(rows, "")
	if err != nil {
		return nil
	}
	chars, cut := 0, 0
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

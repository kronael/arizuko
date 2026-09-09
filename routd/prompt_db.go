package routd

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// prompt_db.go holds the routd.DB query methods that back buildAgentPrompt: the
// system-message flush, the per-topic observed-context window, topic lineage, and
// the group's observe-window override.

// FlushSysMsgs renders the system events queued for folder as <system> lines and
// removes exactly those, in one transaction — at-most-once delivery. The stored
// (source, kind, body) render as (origin, event, body).
//
// The read and the drain share the transaction so no event is rendered into one
// prompt and taken by another, and the drain names the events this read produced
// rather than the folder, so an event queued while the prompt was being built is
// left for the next flush instead of being deleted unread.
func (d *DB) FlushSysMsgs(folder string) string {
	ctx := context.Background()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ""
	}
	defer tx.Rollback()
	msgs, err := d.store().QueuedSystemMessagesTx(ctx, tx, folder)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "<system origin=%q event=%q>%s</system>\n", m.Source, m.Kind, m.Body)
	}
	if err := d.store().DrainSystemMessagesTx(ctx, tx, folder, msgs); err != nil {
		return ""
	}
	if err := tx.Commit(); err != nil {
		return ""
	}
	return b.String()
}

// EnqueueSysMsg appends one system event to folder's queue. emitSystemEvents is
// the producer; FlushSysMsgs (above) is the consumer.
func (d *DB) EnqueueSysMsg(folder, source, kind, body string) error {
	return d.store().EnqueueSystemMessage(folder, source, kind, body)
}

// TopicLineage returns the lineage row for (folder, topic); the prompt path
// consumes only ObservedCursor (parent + forked_at are audit metadata).
// ok=false when no row exists.
func (d *DB) TopicLineage(folder, topic string) (core.TopicLineage, bool) {
	return d.store().TopicLineage(folder, topic)
}

// AdvanceObservedCursor moves a topic's ambient watermark to ts (core.TSLayout
// UTC) and only forward, creating the topic's row when it has none.
//
// It is named for the transition rather than for the verb because the condition
// IS the operation: buildAgentPrompt advances the cursor BEFORE PutSession
// creates the row, so a write that only updated matched nothing on a topic's
// first turns and re-included the whole observed window.
func (d *DB) AdvanceObservedCursor(folder, topic, ts string) error {
	return d.store().AdvanceObservedCursor(folder, topic, ts)
}

// ClearObservedCursor drops a topic's observed watermark, so the next prompt
// fills the topic's ambient window from the cap instead of from a fork stamp.
// The fork path calls it when the child could not inherit the parent transcript
// — without it that child starts its first turn with no context at all.
func (d *DB) ClearObservedCursor(folder, topic string) error {
	return d.store().ClearObservedCursor(folder, topic)
}

// GroupObserveWindow returns the group's stored observe-window override as
// (messages, chars). A NULL key yields -1 so the cfg default wins (only a
// >= 0 value overrides). (-1,-1) when the group has no row.
func (d *DB) GroupObserveWindow(folder string) (int, int) {
	return store.New(d.db).GroupObserveWindow(folder)
}

// WatchedSources returns the source folders folder watches — the observe_group
// ambient join. Empty slice when folder watches nothing.
func (d *DB) WatchedSources(folder string) []string {
	return store.New(d.db).WatchedSources(folder)
}

// ObservedSince returns the trailing window of observed messages for folder
// whose timestamp is strictly greater than cursor (core.TSLayout UTC). Empty
// cursor means "no lower bound; the window cap decides". Capped by maxMsgs and
// maxChars; the oldest messages drop first when the char cap binds.
//
// Two shapes UNION'd: (a) is_observed=1 rows routed to folder; (b) is_observed=0
// primary-delivery rows routed to folder's watched sources (observe_group
// ambient context).
//
// Isolated scheduled runs are excluded from BOTH. They already get their own
// container and their own session so a folder's crons never enter the chat's
// transcript (BUGS L54) — but their rows land routed_to the folder with
// is_observed=1, so the ambient window served every cron prompt back to every
// topic in the folder on its next turn. That re-opened the exact hole L54
// closed, through a second door: on sloth's #trading the observed block was
// dominated by 8-a-day "silent cron" prompts.
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
		       AND is_bot_message = 0 AND content != ''
		       AND sender NOT LIKE '` + core.IsolatedSenderPrefix + `%'` + cursorClause + `
		     UNION ALL
		     SELECT ` + msgReadCols + ` FROM messages
		     WHERE routed_to IN (` + watchedPH + `) AND is_observed = 0
		       AND is_bot_message = 0 AND content != ''
		       AND sender NOT LIKE '` + core.IsolatedSenderPrefix + `%'` + cursorClause + `
		     ORDER BY timestamp DESC
		     LIMIT ?`
	} else {
		if cursor != "" {
			args = append(args, cursor)
		}
		args = append(args, maxMsgs)
		q = `SELECT ` + msgReadCols + ` FROM messages
		     WHERE routed_to = ? AND is_observed = 1
		       AND is_bot_message = 0 AND content != ''
		       AND sender NOT LIKE '` + core.IsolatedSenderPrefix + `%'` + cursorClause + `
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

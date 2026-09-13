package routd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/core"
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
	msgs, err := d.store().QueuedSystemMessages(ctx, folder)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	// The drain claims exactly the events this read produced. A refusal means
	// another flush took one of them, so this caller renders nothing and the
	// events it did not claim stay queued for the reader that names them.
	claimed, err := d.store().DrainSystemMessages(ctx, folder, msgs)
	if err != nil || !claimed {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "<system origin=%q event=%q>%s</system>\n", m.Source, m.Kind, m.Body)
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
	return d.store().GroupObserveWindow(folder)
}

// WatchedSources returns the source folders folder watches — the observe_group
// ambient join. Empty slice when folder watches nothing.
func (d *DB) WatchedSources(folder string) []string {
	return d.store().WatchedSources(folder)
}

// ObservedSince returns the trailing window of observed messages for folder whose
// timestamp is strictly greater than cursor (core.TSLayout UTC). Empty cursor
// means "no lower bound; the window cap decides". Capped by maxMsgs and maxChars;
// the oldest messages drop first when the char cap binds.
//
// Two shapes merged: the folder's own observed rows, and the primary-delivery
// rows of the folders it watches (the observe_group ambient join). Both come out
// of the folder feed, which is the index the `routed_to` write maintains.
//
// Isolated scheduled runs are excluded from BOTH (BUGS L54); store owns that
// filter now, because the feed carries the sender it tests.
func (d *DB) ObservedSince(folder, cursor string, maxMsgs, maxChars int) []core.Message {
	msgs, err := d.store().ObservedSince(context.Background(), folder, cursor,
		maxMsgs, maxChars, d.WatchedSources(folder))
	if err != nil {
		slog.Error("ambient window", "folder", folder, "err", err)
		return nil
	}
	return msgs
}

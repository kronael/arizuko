package routd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/core"
)

// prompt_db.go holds the routd.DB query methods behind buildAgentPrompt: the
// system-message flush, the per-topic observed window, topic lineage and the
// group's observe-window override.

// FlushSysMsgs renders the system events queued for folder as <system> lines
// and drains exactly those — at-most-once. The drain names the events this
// read produced rather than the folder, so an event queued mid-build is left
// for the next flush instead of deleted unread.
func (d *DB) FlushSysMsgs(folder string) string {
	ctx := context.Background()
	msgs, err := d.store().QueuedSystemMessages(ctx, folder)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	// A refusal means another flush took one of them: render nothing.
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

// ObservedSince returns the trailing window of observed messages for folder
// strictly after cursor (empty: the cap decides), capped by maxMsgs/maxChars
// with the oldest dropped first. Three shapes merged: the folder's own observed
// rows, its own bot replies, and the primary-delivery rows of the folders it
// watches, all from the folder feed. Isolated scheduled runs are excluded from
// ALL (BUGS L54);
// store owns that filter.
func (d *DB) ObservedSince(folder, cursor string, maxMsgs, maxChars int) []core.Message {
	msgs, err := d.store().ObservedSince(context.Background(), folder, cursor,
		maxMsgs, maxChars, d.store().WatchedSources(folder))
	if err != nil {
		slog.Error("ambient window", "folder", folder, "err", err)
		return nil
	}
	return msgs
}

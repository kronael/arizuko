package store

// message_activity.go holds the activity feed: the newest messages across every
// chat, the one read that has no coordinate at all.
//
// ONE CONSTANT PARTITION, sorted by the message's own position: answering from
// the chats themselves transferred 10,525 items on krons to show 1,000. It
// carries the PROJECTION, not a pointer, because this page renders a thousand
// rows; the fields are exactly what the HTTP surface serves, and
// `EnrichMessage` rewrites the preview with the body.
//
// RETENTION IS THIRTY DAYS: an index with no drainage is the mistake
// `turn_context` records. Thirty holds about 2,200 items on the busiest
// instance, two full pages of the thousand this feed serves, and the page has
// never asked for a message older than its second page.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes an activity item carries: what the feed renders, and nothing
// else.
const (
	attrActChatJID = "chat_jid"
	attrActSender  = "sender"
	attrActSource  = "source"
	attrActVerb    = "verb"
	attrActPreview = "preview"
	attrActBot     = "is_bot_message"
)

// activityPartition is the one partition the feed lives in.
const activityPartition = OwnerRoutd

// ActivityRetention is how far back the feed keeps a message (file header).
const ActivityRetention = 30 * 24 * time.Hour

// activityPreviewChars is the bound the HTTP surface applies, moved to the
// write so the read cannot widen it.
const activityPreviewChars = 80

// The feed is read newest-first, so its key carries the same ordered lead.
func init() { objstore.OrderedLead(MessageActivity, "", tsWidth) }

// ActivityRow is one line of the cross-chat feed.
type ActivityRow struct {
	Timestamp string
	Source    string
	ChatJID   string
	Sender    string
	Verb      string
	Preview   string
}

// activityWrite renders the feed entry every message takes. No condition: it
// rides the message's own create-if-absent.
func activityWrite(msgKey objstore.Key, m core.Message) objstore.Write {
	attrs := map[string]string{attrActChatJID: m.ChatJID}
	setAttr(attrs, attrActSender, m.Sender)
	setAttr(attrs, attrActSource, m.Source)
	setAttr(attrs, attrActVerb, defaultVerb(m.Verb))
	setAttr(attrs, attrActPreview, truncateRunes(m.Content, activityPreviewChars))
	setFlag(attrs, attrActBot, m.BotMsg)
	return objstore.Write{
		Op: objstore.Put, Entity: MessageActivity,
		Key:   objstore.Key{Partition: activityPartition, Sort: msgKey.Sort},
		Attrs: attrs,
	}
}

// activityKey is the coordinate of one message's feed entry.
func activityKey(msgKey objstore.Key) objstore.Key {
	return objstore.Key{Partition: activityPartition, Sort: msgKey.Sort}
}

// RecentActivity answers the newest messages across every chat, newest first,
// optionally paged by an instant the caller has already read past.
func (s *Store) RecentActivity(ctx context.Context, before string, limit int) ([]ActivityRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	key := objstore.KeyPredicate{}
	if before != "" {
		// A bare instant sorts below every key at it, so this is
		// `timestamp < before`.
		key = objstore.KeyPredicate{Op: objstore.LtKey, Value: before}
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageActivity, Partition: activityPartition, Key: key,
		Descending: true, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read the activity feed: %w", err)
	}
	out := make([]ActivityRow, 0, len(items))
	for _, it := range items {
		ts, _, _ := MsgCoord(it.Key.Sort)
		out = append(out, ActivityRow{
			Timestamp: ts,
			Source:    it.Attrs[attrActSource],
			ChatJID:   it.Attrs[attrActChatJID],
			Sender:    it.Attrs[attrActSender],
			Verb:      it.Attrs[attrActVerb],
			Preview:   it.Attrs[attrActPreview],
		})
	}
	return out, nil
}

// DailyVolume answers how many human messages each of the last `days` days
// carried, newest day first: the usage page's histogram. It reads the feed so
// the window is a key range; bot rows are excluded HERE, by the owner, so no
// caller can widen the count to the agent's own output.
func (s *Store) DailyVolume(ctx context.Context, days int) (map[string]int, error) {
	if days <= 0 {
		days = 7
	}
	from := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageActivity, Partition: activityPartition,
		Key:     objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampAt(from)},
		Where:   []objstore.Cmp{{Attr: attrActBot, Op: objstore.Absent}},
		Project: []string{},
	})
	if err != nil {
		return nil, fmt.Errorf("read the message volume: %w", err)
	}
	out := map[string]int{}
	for _, it := range items {
		ts, _, ok := MsgCoord(it.Key.Sort)
		if !ok || len(ts) < len("2006-01-02") {
			continue
		}
		out[ts[:len("2006-01-02")]]++
	}
	return out, nil
}

// SweepActivity removes feed entries older than the retention window and
// answers how many went. It runs on the hourly housekeeping tick; a delete
// names one item, so the sweep reads the range and then removes it.
func (s *Store) SweepActivity(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-ActivityRetention)
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageActivity, Partition: activityPartition,
		Key:     objstore.KeyPredicate{Op: objstore.LtKey, Value: msgStampAt(cutoff)},
		Project: []string{},
	})
	if err != nil {
		return 0, fmt.Errorf("read the activity feed's tail: %w", err)
	}
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: MessageActivity, Key: it.Key,
		})
	}
	if len(writes) == 0 {
		return 0, nil
	}
	return s.applyInChunks(ctx, "sweep the activity feed", writes)
}

// sortItemsDescending puts items in newest-first key order, for a reader that
// merged two partitions.
func sortItemsDescending(items []objstore.Item) {
	sort.Slice(items, func(i, j int) bool { return items[i].Key.Sort > items[j].Key.Sort })
}

// parseCount reads a stored counter back.
func parseCount(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a count", v)
	}
	return n, nil
}

// indexDeletes names the index entries one message item owns, so a delete of
// the message takes them with it: a pointer left behind is a feed entry the
// reader would drop silently on every read for ever.
func indexDeletes(msg objstore.Item) []objstore.Write {
	out := []objstore.Write{{
		Op: objstore.Delete, Entity: MessageActivity, Key: activityKey(msg.Key),
	}}
	if folder := msg.Attrs[attrMsgRoutedTo]; folder != "" {
		out = append(out, objstore.Write{
			Op: objstore.Delete, Entity: MessageFolders,
			Key: objstore.Key{Partition: folder, Sort: msg.Key.Sort},
		})
	}
	return out
}

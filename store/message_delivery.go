package store

// message_delivery.go holds the mutable half of an outbound row: whether it
// still owes a delivery, and how many attempts it has spent.
//
// `sent` IS THE ABSENCE OF AN ITEM: 98.4% of krons' messages are sent and
// nothing ever deletes a sent row, so a `sent` partition is the whole table
// under another name. The partition is the status and the sort key the
// message's own: `pending` is the retry feed and drains; `failed` is the
// dead-letter set (388 items on krons, 920 on sloth) and drains when an
// operator replays one.
//
// It carries a POINTER, like the folder feed: a second copy of the body is a
// second thing `EnrichMessage` would rewrite. platform_id is NOT here: it is
// written once when the adapter confirms delivery, and `MessageByID` resolves
// a reply's parent by it inside one chat partition.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a delivery item carries: the pointer, and the attempt count.
const (
	attrDelChatJID  = "chat_jid"
	attrDelAttempts = "attempts"
)

// The feed is read oldest-first in time order, so its key carries the same
// ordered lead.
func init() { objstore.OrderedLead(MessageDelivery, "", tsWidth) }

// deliveryKey is the coordinate of one row's delivery state.
func deliveryKey(status string, msgKey objstore.Key) objstore.Key {
	return objstore.Key{Partition: status, Sort: msgKey.Sort}
}

// deliveryWrite renders the delivery item a message takes, and reports whether
// it takes one. A sent row takes none, which is what makes `sent` the absence.
func deliveryWrite(msgKey objstore.Key, m core.Message, status string,
	attempts int) (objstore.Write, bool) {
	if status == "" || status == core.MessageStatusSent {
		return objstore.Write{}, false
	}
	attrs := map[string]string{attrDelChatJID: m.ChatJID}
	if attempts > 0 {
		attrs[attrDelAttempts] = strconv.Itoa(attempts)
	}
	return objstore.Write{
		Op: objstore.Put, Entity: MessageDelivery, Key: deliveryKey(status, msgKey),
		Attrs: attrs,
	}, true
}

// PendingMessage is one row the retry sweep is deciding about: the message, and
// how many attempts it has already spent.
type PendingMessage struct {
	core.Message
	Attempts int
}

// PutOutbound appends one outbound row and the delivery state it owes. A door
// of its own because every other writer appends a row that is already
// delivered. An unknown status is refused rather than stored, so a typo cannot
// park a row in a partition no sweep reads.
func (s *Store) PutOutbound(ctx context.Context, m core.Message, status string) error {
	switch status {
	case core.MessageStatusSent, core.MessageStatusPending, core.MessageStatusFailed:
	default:
		return fmt.Errorf("outbound %s carries the delivery status %q, and the three a "+
			"sweep reads are %q, %q and %q", m.ID, status, core.MessageStatusSent,
			core.MessageStatusPending, core.MessageStatusFailed)
	}
	_, err := s.putMessage(ctx, m, status)
	return err
}

// PendingOutbound answers the bot rows still owed a delivery whose position is
// at or before a cutoff, oldest first: the outbound retry feed.
func (s *Store) PendingOutbound(ctx context.Context, cutoff time.Time,
	limit int) ([]PendingMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusPending,
		Key:   objstore.KeyPredicate{Op: objstore.LtKey, Value: msgStampPast(cutoff)},
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read the pending outbound feed: %w", err)
	}
	return s.pendingFrom(ctx, items)
}

// pendingFrom dereferences delivery items into the messages they point at.
func (s *Store) pendingFrom(ctx context.Context, items []objstore.Item) ([]PendingMessage, error) {
	out := make([]PendingMessage, 0, len(items))
	for _, it := range items {
		m, ok, err := s.messageBehindDelivery(ctx, it)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		n, err := parseCount(it.Attrs[attrDelAttempts])
		if err != nil {
			return nil, fmt.Errorf("%s's attempt count: %w", it.Key.Sort, err)
		}
		out = append(out, PendingMessage{Message: m, Attempts: n})
	}
	return out, nil
}

// messageBehindDelivery fetches the message one delivery item points at. A
// pointer with no message is a row the circuit breaker pruned, and it drops
// out of the feed rather than failing it.
func (s *Store) messageBehindDelivery(ctx context.Context,
	it objstore.Item) (core.Message, bool, error) {
	chatJID := it.Attrs[attrDelChatJID]
	if chatJID == "" {
		return core.Message{}, false, fmt.Errorf("delivery item %s/%s names no chat, so the "+
			"message it owes cannot be read", it.Key.Partition, it.Key.Sort)
	}
	found, ok, err := s.itemAt(ctx, Messages, objstore.Key{Partition: chatJID, Sort: it.Key.Sort})
	if err != nil || !ok {
		return core.Message{}, false, err
	}
	return messageFrom(found), true, nil
}

// MarkDelivered records that the platform accepted a row: the delivery item goes
// and the platform's own id lands on the message, in one batch.
func (s *Store) MarkDelivered(ctx context.Context, m core.Message, platformID string) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	writes := []objstore.Write{{
		Op: objstore.Delete, Entity: MessageDelivery,
		Key: deliveryKey(core.MessageStatusPending, key),
	}}
	if platformID != "" {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: key,
			Attrs: map[string]string{AttrMsgPlatformID: platformID},
		})
	}
	return s.WriteEntities(ctx, nil, writes...)
}

// DeadLetter moves a row from the retry feed to the dead-letter set: the
// terminal state, where no sweep sends it again and the row keeps its content
// so the operator can read what was lost.
func (s *Store) DeadLetter(ctx context.Context, m core.Message, attempts int) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	attrs := map[string]string{attrDelChatJID: m.ChatJID}
	if attempts > 0 {
		attrs[attrDelAttempts] = strconv.Itoa(attempts)
	}
	// Two items, one batch: a row that left `pending` without arriving in
	// `failed` would be a message nothing owns.
	return s.WriteEntities(ctx, nil,
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery,
			Key: deliveryKey(core.MessageStatusPending, key)},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery,
			Key: deliveryKey(core.MessageStatusFailed, key), Attrs: attrs})
}

// RecordDeliveryFailure counts one transient delivery failure and dead-letters
// the row when the count reaches maxAttempts. It answers the new count and
// whether the row died. THE COUNT IS objstore.Increment: the engine decides
// the winner and the caller gets the new value without a second read. A
// missing item is a row another sweep already resolved, not a failure to count.
func (s *Store) RecordDeliveryFailure(ctx context.Context, m core.Message,
	maxAttempts int) (int, bool, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return 0, false, err
	}
	item := deliveryKey(core.MessageStatusPending, key)
	// The attempt count is absent until the first failure, so the first
	// failure seeds it rather than incrementing nothing.
	if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: MessageDelivery, Key: item,
		Attrs: map[string]string{attrDelAttempts: "0"},
		Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: attrDelAttempts, Op: objstore.Absent}}},
	}); err != nil {
		return 0, false, fmt.Errorf("open %s's attempt count: %w", m.ID, err)
	}
	n, committed, err := s.incrementEntity(ctx, MessageDelivery, item, attrDelAttempts, 1, nil, nil)
	if err != nil {
		return 0, false, fmt.Errorf("count a delivery failure on %s: %w", m.ID, err)
	}
	if !committed {
		return 0, false, fmt.Errorf("%s's attempt count refused its own increment", m.ID)
	}
	if n < maxAttempts {
		return n, false, nil
	}
	return n, true, s.DeadLetter(ctx, m, n)
}

// DeadLetters answers the outbound rows routd gave up on, newest first,
// optionally narrowed to a folder subtree. THE FOLDER FILTER IS THE READER'S:
// the set is a few hundred items. A subtree is a prefix on a path
// (groupfolder.Contains), never LIKE: `_` and `%` are legal in a folder name,
// and LIKE hands one folder's failed messages to a neighbour's grant (BUGS
// L252). folder=="" is every folder; the caller's gate decides whether it may
// ask for that.
func (s *Store) DeadLetters(ctx context.Context, folder string, limit int) ([]PendingMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusFailed, Descending: true,
	})
	if err != nil {
		return nil, fmt.Errorf("read the dead letters: %w", err)
	}
	out := make([]PendingMessage, 0, limit)
	for _, it := range items {
		if len(out) >= limit {
			break
		}
		m, ok, err := s.messageBehindDelivery(ctx, it)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if folder != "" && !groupfolder.Contains(folder, m.RoutedTo) {
			continue
		}
		n, err := parseCount(it.Attrs[attrDelAttempts])
		if err != nil {
			return nil, fmt.Errorf("%s's attempt count: %w", it.Key.Sort, err)
		}
		out = append(out, PendingMessage{Message: m, Attempts: n})
	}
	return out, nil
}

// DeadLetterByID answers one dead-lettered row, for the containment check a
// replay needs. It walks the dead-letter set rather than resolving an id to a
// coordinate: that set IS the list the operator's surface rendered to get the
// id.
func (s *Store) DeadLetterByID(ctx context.Context, id string) (core.Message, bool) {
	if id == "" {
		return core.Message{}, false
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusFailed,
	})
	if err != nil {
		return core.Message{}, false
	}
	for _, it := range items {
		if _, got, ok := MsgCoord(it.Key.Sort); !ok || got != id {
			continue
		}
		m, found, err := s.messageBehindDelivery(ctx, it)
		if err != nil || !found {
			return core.Message{}, false
		}
		return m, true
	}
	return core.Message{}, false
}

// ReplayDeadLetter puts one dead-lettered row back on the retry feed with a
// fresh attempt budget, which is exactly what PendingOutbound selects: no
// second delivery path exists. A row that is not dead-lettered matches nothing,
// and the batch's refusal says so rather than a count of zero read as success.
func (s *Store) ReplayDeadLetter(ctx context.Context, m core.Message) (bool, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(ctx, nil,
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery,
			Key:  deliveryKey(core.MessageStatusFailed, key),
			Cond: objstore.Cond{Item: objstore.ItemPresent}},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery,
			Key:   deliveryKey(core.MessageStatusPending, key),
			Attrs: map[string]string{attrDelChatJID: m.ChatJID}})
}

// CountPendingOutbound answers how many rows still owe a delivery: the /status
// surface's number.
func (s *Store) CountPendingOutbound(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusPending, Project: []string{},
	})
	return len(items), err
}

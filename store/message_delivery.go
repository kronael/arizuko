package store

// message_delivery.go holds the mutable half of an outbound row: whether it
// still owes a delivery, and how many attempts it has spent.
//
// `sent` IS THE ABSENCE OF AN ITEM. The first shape of this entity partitioned
// the status and let the queue drain, and the rows say it does not: 98.4% of
// krons' messages are `sent` (23,625 of 24,013), `pending` holds zero, and
// nothing in the tree ever deletes a sent or a failed row — so a `sent`
// partition is the whole table under another name, and it never sheds one. The
// live schema already knew: `idx_messages_status` is partial, `WHERE status !=
// 'sent'`. So an item exists only while the row is NOT sent, which is objstore's
// "absent is the only null" applied to a status.
//
// THE PARTITION IS THE STATUS AND THE SORT KEY IS THE MESSAGE'S OWN. `pending`
// is the retry feed and genuinely drains — a delivery either lands, which
// removes the item, or dies, which moves it. `failed` is the dead-letter set,
// 388 items on krons, 81 on marinade and 920 on sloth, and it drains when an
// operator replays one.
//
// IT CARRIES A POINTER, like the folder feed and for the same reason: the
// message's body lives in `messages` and a second copy is a second thing
// `EnrichMessage` would have to rewrite. The item holds the chat and the attempt
// count, which is what the sweep reads before it decides.
//
// platform_id IS NOT HERE. It is written once, by one statement, when the
// adapter confirms delivery, and no non-bot row on any instance carries one — so
// it is the platform's identity for the row rather than state that changes.
// Moving it would also cost a live read: `MessageByID` resolves a reply's parent
// by platform id inside one chat partition, and a status partition turns that
// into a scan of every message ever sent.

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

// PutOutbound appends one outbound row and the delivery state it owes.
//
// It is a door of its own rather than a status field on PutMessage, because the
// two are different intents: every other writer appends a row that is already
// delivered, and this one appends a row that still owes one. A status this does
// not recognise is refused rather than stored, so a typo cannot park a row in a
// partition no sweep reads.
func (s *Store) PutOutbound(ctx context.Context, m core.Message, status string) error {
	switch status {
	case core.MessageStatusSent, core.MessageStatusPending, core.MessageStatusFailed:
	default:
		return fmt.Errorf("outbound %s carries the delivery status %q, and the three a "+
			"sweep reads are %q, %q and %q", m.ID, status, core.MessageStatusSent,
			core.MessageStatusPending, core.MessageStatusFailed)
	}
	return s.putMessage(ctx, m, status)
}

// PendingOutbound answers the bot rows still owed a delivery whose position is
// at or before a cutoff, oldest first — the outbound retry feed.
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
// pointer with no message is a row the circuit breaker pruned, and it drops out
// of the feed rather than failing it.
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

// DeadLetter moves a row from the retry feed to the dead-letter set. It is the
// terminal state: no sweep sends it again, and the row keeps its content so the
// operator can read what was lost.
func (s *Store) DeadLetter(ctx context.Context, m core.Message, attempts int) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	attrs := map[string]string{attrDelChatJID: m.ChatJID}
	if attempts > 0 {
		attrs[attrDelAttempts] = strconv.Itoa(attempts)
	}
	// The two writes name two items, so they ride one batch: a row that left
	// `pending` without arriving in `failed` would be a message nothing owns.
	return s.WriteEntities(ctx, nil,
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery,
			Key: deliveryKey(core.MessageStatusPending, key)},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery,
			Key: deliveryKey(core.MessageStatusFailed, key), Attrs: attrs})
}

// RecordDeliveryFailure counts one transient delivery failure and dead-letters
// the row when the count reaches maxAttempts. It answers the new count and
// whether the row died.
//
// THE COUNT IS objstore.Increment, which is the read-modify-write the statement
// it replaces did with `delivery_attempts = delivery_attempts + 1 … RETURNING`:
// the engine decides the winner and the caller gets the new value without a
// second read. A missing item is a row another sweep already resolved, which is
// not a failure to count.
func (s *Store) RecordDeliveryFailure(ctx context.Context, m core.Message,
	maxAttempts int) (int, bool, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return 0, false, err
	}
	item := deliveryKey(core.MessageStatusPending, key)
	// The attempt count is absent until the first failure, because a row that has
	// not been retried carries no count — so the first failure seeds it rather
	// than incrementing nothing.
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
// optionally narrowed to a folder subtree.
//
// THE FOLDER FILTER IS THE READER'S. The dead-letter set is one partition of a
// few hundred items — 388 on krons, 920 on sloth — so narrowing it here costs a
// walk of that set rather than an index. A subtree is a prefix on a path and a
// grant on `acme` covers `acme/support`, which groupfolder.Contains decides; the
// predicate is never LIKE, because `_` and `%` are legal in a folder name and
// LIKE hands one folder's failed messages to a neighbour's grant (BUGS L252).
//
// folder=="" is every folder, which is the operator-wide read; the caller's gate
// decides whether it may ask for that.
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
// replay needs before it touches anything.
//
// IT WALKS THE DEAD-LETTER SET rather than resolving an id to a coordinate,
// because that set IS the list the operator's surface rendered to get the id.
// Building an id-to-coordinate index for a caller that is already holding the
// answer is the index that earns nothing.
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
// fresh attempt budget, which is exactly what PendingOutbound selects, so
// routd's EXISTING outbound sweep sends it. No second delivery path exists.
//
// It reports whether a row moved. A row that is not dead-lettered — already
// sent, or replayed a moment ago by someone else — matches nothing, and the
// batch's refusal is what says so rather than a count of zero read as success.
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

// CountPendingOutbound answers how many rows still owe a delivery — the /status
// surface's number.
func (s *Store) CountPendingOutbound(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusPending, Project: []string{},
	})
	return len(items), err
}

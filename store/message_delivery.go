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
// confirmed on delivery and removed on status deletion. `MessageByID` resolves
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
	attrs := statusDeliveryAttrs(m)
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
	Attempts  int
	Operation string
	QueuedAt  time.Time
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
	_, err := s.putMessage(ctx, m, status, false)
	return err
}

// PutOutboundOnce keeps an exact, deterministic coordinate across delivery retries.
func (s *Store) PutOutboundOnce(ctx context.Context, m core.Message) (bool, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return false, err
	}
	_, exists, err := s.itemAt(ctx, Messages, key)
	if err != nil || exists {
		return false, err
	}
	_, err = s.putMessage(ctx, m, core.MessageStatusPending, true)
	return err == nil, err
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
		if m.StatusRevision > 0 && it.Attrs["revision"] != strconv.Itoa(m.StatusRevision) {
			continue
		}
		n, err := parseCount(it.Attrs[attrDelAttempts])
		if err != nil {
			return nil, fmt.Errorf("%s's attempt count: %w", it.Key.Sort, err)
		}
		out = append(out, PendingMessage{Message: m, Attempts: n, Operation: it.Attrs["operation"], QueuedAt: deliveryQueuedAt(it, m)})
	}
	return out, nil
}

// messageBehindDelivery fetches the message one delivery item points at. A
// pointer with no message is a row removed by cleanup, and it drops
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
	if m.StatusRevision > 0 {
		return s.ackStatus(ctx, m, platformID)
	}
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
	attrs := statusDeliveryAttrs(m)
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
		if m.StatusRevision > 0 && it.Attrs["revision"] != strconv.Itoa(m.StatusRevision) {
			continue
		}
		n, err := parseCount(it.Attrs[attrDelAttempts])
		if err != nil {
			return nil, fmt.Errorf("%s's attempt count: %w", it.Key.Sort, err)
		}
		out = append(out, PendingMessage{Message: m, Attempts: n, Operation: it.Attrs["operation"], QueuedAt: deliveryQueuedAt(it, m)})
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
	checks := []objstore.Check{}
	if m.StatusRevision > 0 {
		checks = append(checks, objstore.Check{Entity: Messages, Key: key, Cond: statusRevisionGuard(m)})
	}
	return s.TryWriteEntities(ctx, checks,
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery,
			Key:  deliveryKey(core.MessageStatusFailed, key),
			Cond: objstore.Cond{Item: objstore.ItemPresent}},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery,
			Key:   deliveryKey(core.MessageStatusPending, key),
			Attrs: statusDeliveryAttrs(m)})
}

// CountPendingOutbound answers how many rows still owe a delivery: the /status
// surface's number.
func (s *Store) CountPendingOutbound(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageDelivery, Partition: core.MessageStatusPending, Project: []string{},
	})
	return len(items), err
}

func statusDeliveryAttrs(m core.Message) map[string]string {
	attrs := map[string]string{attrDelChatJID: m.ChatJID}
	if m.StatusRevision > 0 {
		attrs["queued_at"] = core.TS(time.Now())
		attrs["revision"] = strconv.Itoa(m.StatusRevision)
		switch {
		case m.StatusPhase == "cleared":
			attrs["operation"] = "delete"
		case m.PlatformID != "":
			attrs["operation"] = "edit"
		default:
			attrs["operation"] = "send"
		}
	}
	return attrs
}

func statusRevisionGuard(m core.Message) objstore.Cond {
	cmp := objstore.Cmp{Attr: "status_revision", Op: objstore.Absent}
	if m.StatusRevision > 0 {
		cmp.Op, cmp.Value = objstore.Eq, strconv.Itoa(m.StatusRevision)
	}
	return objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{cmp}}
}

// QueueStatus saves desired content and its delivery operation in one batch.
// The caller serializes updates for the turn. An absent row is appended once.
func (s *Store) QueueStatus(ctx context.Context, m core.Message, content, phase string) error {
	if m.StatusRevision == 0 {
		if _, exists := s.MessageByID(m.ChatJID, m.ID); !exists {
			m.Content, m.StatusPhase, m.StatusRevision = content, phase, 1
			_, err := s.putMessage(ctx, m, core.MessageStatusPending, true)
			return err
		}
	}
	if m.Content == content && m.StatusPhase == phase {
		return nil
	}
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	guard := statusRevisionGuard(m)
	platform := objstore.Cmp{Attr: AttrMsgPlatformID, Op: objstore.Eq, Value: m.PlatformID}
	if m.PlatformID == "" {
		platform.Op = objstore.Absent
	}
	guard.Where = append(guard.Where, platform)
	next := m
	next.Content, next.StatusPhase, next.StatusRevision = content, phase, m.StatusRevision+1
	attrs := map[string]string{attrMsgContent: content, "status_phase": phase, "status_revision": strconv.Itoa(next.StatusRevision)}
	if m.StatusPhase == "cleared" && m.PlatformID == "" && phase != "cleared" {
		attrs["status_send_key"] = m.ID + "-" + strconv.Itoa(next.StatusRevision)
	}
	return s.WriteEntities(ctx, nil,
		objstore.Write{Op: objstore.Update, Entity: Messages, Key: key,
			Attrs: attrs, Cond: guard},
		objstore.Write{Op: objstore.Update, Entity: MessageActivity, Key: activityKey(key), Attrs: map[string]string{attrActPreview: truncateRunes(content, activityPreviewChars)}},
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusFailed, key)},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusPending, key), Attrs: statusDeliveryAttrs(next)})
}

func (s *Store) ackStatus(ctx context.Context, m core.Message, platformID string) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	for {
		it, exists, err := s.itemAt(ctx, Messages, key)
		if err != nil || !exists {
			return err
		}
		current := messageFrom(it)
		writes := []objstore.Write{{Op: objstore.Update, Entity: Messages, Key: key, Cond: statusRevisionGuard(current)}}
		if current.StatusPhase == "cleared" && current.StatusRevision == m.StatusRevision {
			writes[0].Remove = []string{AttrMsgPlatformID}
		}
		if platformID != "" {
			writes[0].Attrs = map[string]string{AttrMsgPlatformID: platformID}
		}
		if current.StatusRevision != m.StatusRevision {
			if m.PlatformID != "" || platformID == "" {
				return nil
			}
			current.PlatformID = platformID
			writes = append(writes, objstore.Write{Op: objstore.Put, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusPending, key), Attrs: statusDeliveryAttrs(current)})
		} else {
			writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusPending, key)})
		}
		ok, err := s.TryWriteEntities(ctx, nil, writes...)
		if err != nil || ok {
			return err
		}
	}
}

// FailStatus counts only the attempted revision and retains the operation on dead letters.
func (s *Store) FailStatus(ctx context.Context, pending PendingMessage, permanent bool, maxAttempts int) error {
	key, err := messageKeyOf(pending.Message)
	if err != nil {
		return err
	}
	n := pending.Attempts + 1
	attrs := statusDeliveryAttrs(pending.Message)
	attrs[attrDelAttempts] = strconv.Itoa(n)
	if permanent {
		delete(attrs, attrDelAttempts)
	}
	attrs["queued_at"] = core.TS(pending.QueuedAt)
	target := core.MessageStatusPending
	if permanent || n >= maxAttempts {
		target = core.MessageStatusFailed
	}
	cond := objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{{Attr: "revision", Op: objstore.Eq, Value: strconv.Itoa(pending.StatusRevision)}}}
	count := objstore.Cmp{Attr: attrDelAttempts, Op: objstore.Eq, Value: strconv.Itoa(pending.Attempts)}
	if pending.Attempts == 0 {
		count.Op = objstore.Absent
	}
	cond.Where = append(cond.Where, count)
	write := objstore.Write{Op: objstore.Put, Entity: MessageDelivery, Key: deliveryKey(target, key), Attrs: attrs}
	if target == core.MessageStatusPending {
		write.Op = objstore.Update
		write.Cond = cond
	}
	writes := []objstore.Write{write}
	if target == core.MessageStatusFailed {
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusPending, key), Cond: cond})
	}
	_, err = s.TryWriteEntities(ctx, nil, writes...)
	return err
}

func deliveryQueuedAt(it objstore.Item, m core.Message) time.Time {
	at, err := time.Parse(time.RFC3339Nano, it.Attrs["queued_at"])
	if err != nil {
		return m.Timestamp
	}
	return at
}

// PendingDelivery reads the currently owed operation at a message's coordinate.
func (s *Store) PendingDelivery(ctx context.Context, m core.Message) (PendingMessage, bool, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return PendingMessage{}, false, err
	}
	it, ok, err := s.itemAt(ctx, MessageDelivery, deliveryKey(core.MessageStatusPending, key))
	if err != nil || !ok {
		return PendingMessage{}, ok, err
	}
	rows, err := s.pendingFrom(ctx, []objstore.Item{it})
	if err != nil || len(rows) == 0 {
		return PendingMessage{}, false, err
	}
	return rows[0], true, nil
}

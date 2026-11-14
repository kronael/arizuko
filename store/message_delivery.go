package store

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

// The attributes a delivery item carries: the pointer, and the attempt count.
const (
	attrDelChatJID  = "chat_jid"
	attrDelAttempts = "attempts"
)

func init() { objstore.OrderedLead(MessageDelivery, "", tsWidth) }

func deliveryKey(status string, msgKey objstore.Key) objstore.Key {
	return objstore.Key{Partition: status, Sort: msgKey.Sort}
}

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
	Attempts      int
	Operation     string
	QueuedAt      time.Time
	NextAttemptAt time.Time
	ExpiresAt     time.Time
	Notice        string
	observed      objstore.Item
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
	_, err := s.putMessage(ctx, m, status, messageOptions{})
	return err
}

// PutOutboundOnce keeps an exact, deterministic coordinate across delivery retries.
func (s *Store) PutOutboundOnce(ctx context.Context, m core.Message) (bool, error) {
	_, exists, err := s.messageAt(ctx, m.ChatJID, m.ID)
	if err != nil || exists {
		return false, err
	}
	_, err = s.putMessage(ctx, m, core.MessageStatusPending, messageOptions{exact: true})
	return err == nil, err
}

// PendingOutbound selects deliveries whose next attempt is due, in deadline order.
func (s *Store) PendingOutbound(ctx context.Context, now time.Time,
	limit int) ([]PendingMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	items, err := objstore.IndexItems(ctx, s.Obj(), "outbound/due", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, limit)
	if err != nil {
		return nil, fmt.Errorf("read the pending outbound feed: %w", err)
	}
	return s.pendingFrom(ctx, items)
}

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
		due, err := deliveryDueAt(it)
		if err != nil {
			return nil, err
		}
		expires, err := deliveryExpiry(it)
		if err != nil {
			return nil, err
		}
		out = append(out, PendingMessage{ExpiresAt: expires, Notice: it.Attrs["last_error"], Message: m, Attempts: n, Operation: it.Attrs["operation"], QueuedAt: deliveryQueuedAt(it, m), NextAttemptAt: due, observed: it})
	}
	return out, nil
}

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
	key, err := s.deliveryMessageKey(ctx, m)
	if err != nil {
		return err
	}
	writes := []objstore.Write{{
		Op: objstore.Delete, Entity: MessageDelivery,
		Key: deliveryKey(core.MessageStatusPending, key),
	}, {Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusFailed, key)}}
	if platformID != "" {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: key,
			Attrs: map[string]string{AttrMsgPlatformID: platformID},
		})
	}
	return s.WriteEntities(ctx, nil, writes...)
}

// DeadLetter moves an observed delivery to the terminal state.
func (s *Store) DeadLetter(ctx context.Context, pending PendingMessage, attempts int) error {
	if attempts == 0 {
		attempts = pending.Attempts
	}
	_, err := s.transitionDelivery(ctx, pending, retry.Decision{Failures: attempts, Stop: true})
	return err
}

func (s *Store) RecordDeliveryFailure(ctx context.Context, pending PendingMessage,
	failure retry.Failure) (int, bool, error) {
	decision := retry.Next(pending.Attempts, failure, time.Now(), pending.ExpiresAt)
	ok, err := s.transitionDelivery(ctx, pending, decision)
	if !ok || err != nil {
		return 0, false, err
	}
	return decision.Failures, decision.Stop, nil
}

func (s *Store) transitionDelivery(ctx context.Context, pending PendingMessage, decision retry.Decision) (bool, error) {
	it := pending.observed
	attrs := maps.Clone(it.Attrs)
	if attrs == nil {
		return false, fmt.Errorf("delivery %s has no observed state", pending.ID)
	}
	if decision.Failures > 0 {
		attrs[attrDelAttempts] = strconv.Itoa(decision.Failures)
	}
	if decision.Stop {
		delete(attrs, "next_attempt_at")
		attrs["last_error"] = retry.Notice("Delivery failed", decision)
	} else {
		attrs["next_attempt_at"] = core.TS(decision.NextAt)
		delete(attrs, "last_error")
	}
	cond := objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{attrIs(objstore.IndexRevision, it.Attrs[objstore.IndexRevision])}}
	write := objstore.Write{Op: objstore.Update, Entity: MessageDelivery, Key: it.Key, Attrs: attrs, Remove: []string{"last_error"}, Cond: cond}
	writes := []objstore.Write{write}
	if decision.Stop {
		writes[0] = objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: it.Key, Cond: cond}
		write.Op = objstore.Put
		write.Key.Partition = core.MessageStatusFailed
		write.Remove = nil
		write.Cond = objstore.Cond{}
		writes = append(writes, write)
	}
	return s.TryWriteEntities(ctx, nil, writes...)
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
		var due time.Time
		if it.Attrs["next_attempt_at"] != "" {
			due, err = deliveryDueAt(it)
			if err != nil {
				return nil, err
			}
		}
		expires, err := deliveryExpiry(it)
		if err != nil {
			return nil, err
		}
		out = append(out, PendingMessage{ExpiresAt: expires, Notice: it.Attrs["last_error"], Message: m, Attempts: n, Operation: it.Attrs["operation"], QueuedAt: deliveryQueuedAt(it, m), NextAttemptAt: due, observed: it})
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
	key, err := s.deliveryMessageKey(ctx, m)
	if err != nil {
		return false, err
	}
	checks := []objstore.Check{}
	if m.StatusRevision > 0 {
		checks = append(checks, objstore.Check{Entity: Messages, Key: key, Cond: statusRevisionGuard(m)})
	}
	failed, exists, err := s.itemAt(ctx, MessageDelivery, deliveryKey(core.MessageStatusFailed, key))
	if err != nil || !exists {
		return false, err
	}
	if value := failed.Attrs["expires_at"]; value != "" {
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return false, err
		}
		if !time.Now().Before(at) {
			return false, fmt.Errorf("outbound message has expired")
		}
	}
	attrs := statusDeliveryAttrs(m)
	attrs["next_attempt_at"] = core.NowTS()
	setAttr(attrs, "expires_at", failed.Attrs["expires_at"])
	return s.TryWriteEntities(ctx, checks,
		objstore.Write{Op: objstore.Delete, Entity: MessageDelivery,
			Key: deliveryKey(core.MessageStatusFailed, key),
			Cond: objstore.Cond{Item: objstore.ItemPresent,
				Where: []objstore.Cmp{attrIs(objstore.IndexRevision, failed.Attrs[objstore.IndexRevision])}}},
		objstore.Write{Op: objstore.Put, Entity: MessageDelivery,
			Key:   deliveryKey(core.MessageStatusPending, key),
			Attrs: attrs})
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
	attrs := map[string]string{attrDelChatJID: m.ChatJID, "next_attempt_at": core.TS(retry.Next(0, retry.Failure{Kind: "transient"}, m.Timestamp, time.Time{}).NextAt)}
	if m.StatusRevision > 0 {
		attrs["queued_at"] = core.TS(time.Now())
		attrs["next_attempt_at"] = core.TS(retry.Next(0, retry.Failure{Kind: "transient"}, time.Now(), time.Time{}).NextAt)
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
			_, err := s.putMessage(ctx, m, core.MessageStatusPending, messageOptions{exact: true})
			return err
		}
	}
	if m.Content == content && m.StatusPhase == phase {
		return nil
	}
	key, err := s.deliveryMessageKey(ctx, m)
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
	key, err := s.deliveryMessageKey(ctx, m)
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
			writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusPending, key)},
				objstore.Write{Op: objstore.Delete, Entity: MessageDelivery, Key: deliveryKey(core.MessageStatusFailed, key)})
		}
		ok, err := s.TryWriteEntities(ctx, nil, writes...)
		if err != nil || ok {
			return err
		}
	}
}

// FailStatus counts only the attempted revision and retains the operation on dead letters.
func (s *Store) FailStatus(ctx context.Context, pending PendingMessage, failure retry.Failure) error {
	_, _, err := s.RecordDeliveryFailure(ctx, pending, failure)
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
	key, err := s.deliveryMessageKey(ctx, m)
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

func deliveryDueAt(it objstore.Item) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, it.Attrs["next_attempt_at"])
}

func legacyDeliveryDueAt(it objstore.Item) (time.Time, error) {
	stamp, _, _ := MsgCoord(it.Key.Sort)
	if queued := it.Attrs["queued_at"]; queued != "" {
		stamp = queued
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, err
	}
	n, err := parseCount(it.Attrs[attrDelAttempts])
	if err != nil {
		return time.Time{}, err
	}
	delay := 30 * time.Second
	for range n {
		delay = min(delay*2, time.Hour)
		if delay == time.Hour {
			break
		}
	}
	return at.Add(delay), nil
}

func (s *Store) deliveryMessageKey(ctx context.Context, m core.Message) (objstore.Key, error) {
	it, exists, err := s.messageAt(ctx, m.ChatJID, m.ID)
	if err != nil {
		return objstore.Key{}, err
	}
	if !exists {
		return objstore.Key{}, fmt.Errorf("message %s in %s is missing", m.ID, m.ChatJID)
	}
	return it.Key, nil
}

func deliveryExpiry(it objstore.Item) (time.Time, error) {
	if value := it.Attrs["expires_at"]; value != "" {
		return time.Parse(time.RFC3339Nano, value)
	}
	return time.Time{}, nil
}

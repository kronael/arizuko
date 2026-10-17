package chanstore

// ack_mark.go holds every read and write of `ack_marks`: one item per chat that
// stands under an acknowledgement mark, the `👀` an adapter without a native
// presence indicator puts on the message that started a turn
// (specs/6/46-turn-progress.md §"The mark, implemented once").
//
// The mark exists on the PLATFORM and the adapter must find it again after a
// restart, because the end of the turn arrives later from routd and says only
// the chat. Without a record a crash leaves the reaction standing for good:
// the process that knew which comment carried it is gone.
//
// WHY A NEW ENTITY. None of the three before it fits. `outbound_dedup` is
// addressed by routd's idempotency key and is never deleted; `inbound_pending`
// is a queue of payloads, drained by re-delivery; `file_urls` is an id-to-URL
// map nothing sweeps. A mark is addressed by CHAT, deleted the moment its turn
// ends, and read as a WHOLE at boot — a lifecycle none of them has.

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The attributes an item carries. `target` is the message the mark stands on;
// `handle` is what the platform needs to remove it and is absent when the
// platform removes by coordinate (Slack names the message and the emoji,
// GitHub names a reaction id). `set_at` is core.TS and only the boot sweep
// reads it, in Go, so it is not declared ordered.
const (
	attrMarkTarget = "target"
	attrMarkHandle = "handle"
	attrMarkSetAt  = "set_at"
)

// markPartition is the one partition every mark lives in.
//
// THE PARTITION IS A CONSTANT for `inbound_pending`'s reason: the boot sweep
// asks about the WHOLE set, and a query without a partition is refused by every
// driver. The set stays small for `inbound_pending`'s other reason too — it
// DRAINS. A mark is deleted when its turn ends, so the steady state is one item
// per chat with a turn running.
const markPartition = "marks"

// markKey is the coordinate of one chat's mark. The sort key is the chat JID,
// so a chat has at most one mark and a second Mark replaces the first.
func markKey(jid string) objstore.Key { return objstore.Key{Partition: markPartition, Sort: jid} }

// A Mark is one standing acknowledgement mark as the adapter reads it back.
type Mark struct {
	ChatJID string
	Target  string
	Handle  string
	SetAt   string
}

// SetMark records that a mark stands on target for jid, replacing any record
// for that chat. setAt is core.TS.
//
// The write is UNCONDITIONAL: the adapter puts one mark per chat at a time and
// removes the earlier one first, so a record already there is stale by
// construction.
func (s *Store) SetMark(ctx context.Context, jid, target, handle, setAt string) error {
	attrs := map[string]string{attrMarkTarget: target, attrMarkSetAt: setAt}
	if handle != "" {
		attrs[attrMarkHandle] = handle
	}
	if _, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: AckMark, Key: markKey(jid), Attrs: attrs,
	}}); err != nil {
		return fmt.Errorf("record the mark for %s: %w", jid, err)
	}
	return nil
}

// MarkFor answers the mark standing for jid, writing nothing. A chat with none
// is a normal answer.
func (s *Store) MarkFor(ctx context.Context, jid string) (Mark, bool, error) {
	it, held, err := s.point(ctx, AckMark, markKey(jid))
	if err != nil {
		return Mark{}, false, fmt.Errorf("read the mark for %s: %w", jid, err)
	}
	if !held {
		return Mark{}, false, nil
	}
	return markOf(it), true, nil
}

// Marks answers every standing mark: what the boot sweep walks.
func (s *Store) Marks(ctx context.Context) ([]Mark, error) {
	items, err := s.s.Query(ctx, objstore.Query{Entity: AckMark, Partition: markPartition})
	if err != nil {
		return nil, fmt.Errorf("read the standing marks: %w", err)
	}
	marks := make([]Mark, 0, len(items))
	for _, it := range items {
		marks = append(marks, markOf(it))
	}
	return marks, nil
}

// DropMark forgets the mark for jid once the platform no longer shows it. The
// delete carries no condition, so dropping a chat with none is the no-op a
// delete means on every engine.
func (s *Store) DropMark(ctx context.Context, jid string) error {
	if _, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Delete, Entity: AckMark, Key: markKey(jid),
	}}); err != nil {
		return fmt.Errorf("drop the mark for %s: %w", jid, err)
	}
	return nil
}

func markOf(it objstore.Item) Mark {
	return Mark{
		ChatJID: it.Key.Sort,
		Target:  it.Attrs[attrMarkTarget],
		Handle:  it.Attrs[attrMarkHandle],
		SetAt:   it.Attrs[attrMarkSetAt],
	}
}

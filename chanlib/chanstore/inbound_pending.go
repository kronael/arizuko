package chanstore

// inbound_pending.go holds every read and write of `inbound_pending`: one item
// per inbound message routd did not accept, held by the adapter until it does.
//
// Only adapters that CONSUME the platform event on arrival keep these items.
// slakd answers Slack 200 before routd is called (Slack demands the ack inside
// 3s and redelivers on non-2xx, so deferring the ack turns one slow routd into
// several copies of the message); discd's gateway hands each event over once;
// gitd's webhook is delivered once. Neither platform will offer the message
// again, so a failed routd call used to lose it with one ERROR line as the trace
// (BUGS L25).
//
// The id is the adapter's stable message id — the same value routd stores as the
// messages primary key, so a re-delivery of a row routd already has is a no-op
// rather than a duplicate.

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/kronael/arizuko/objstore"
)

// The attributes an item carries.
const (
	attrPayload       = "payload"
	attrAttempts      = "attempts"
	attrFirstSeenAt   = "first_seen_at"
	attrNextAttemptAt = "next_attempt_at"
	attrLastError     = "last_error"
)

// pendingPartition is the one partition every held message lives in.
//
// THE PARTITION IS A CONSTANT here, which is the opposite of the choice
// `outbound_dedup` made two files over, and the queries are what decide it. Two
// of the reads below ask about the WHOLE queue — "every message due now" and
// "how many are we holding" — and a partition is the coordinate a caller filters
// on (6/37 rule 1). There is nothing to filter by: the database already IS one
// adapter's queue, so partitioning by the message id (emaid's shape) would leave
// the sweep with no partition to name, and a query without one is refused by
// every driver.
//
// What makes the whole-partition scan affordable is DRAINAGE, which is what the
// dedup ledger lacks: a message that routd accepts or permanently refuses is
// deleted, so the steady state is empty — measured 2026-09-07, zero rows in both
// live teled databases — and the queue only fills while routd is down. This is
// authd's single-partition shape WITH authd's excuse.
//
// The SORT KEY IS THE MESSAGE ID because id-uniqueness is the guarantee: a row
// already held keeps its attempt count and its backoff, and that is enforced by
// a Put under ItemAbsent on this key. A time-valued sort key would make the
// sweep's read a cheap range, and it would also let one id land twice — Slack
// retries a non-2xx ack — and be delivered twice. The cheap read is not worth
// the duplicate.
const pendingPartition = "inbound"

// A Held message is one inbound the adapter is still holding, as the sweep reads
// it.
type Held struct {
	ID       string
	Payload  string
	Attempts int
}

// pendingKey is the coordinate of one held message.
func pendingKey(id string) objstore.Key {
	return objstore.Key{Partition: pendingPartition, Sort: id}
}

// Retain holds one inbound message so the sweep can re-deliver it.
//
// A MESSAGE ALREADY HELD IS LEFT EXACTLY AS IT IS: the write is a Put under
// ItemAbsent, so a refusal is the intended outcome and not an error. A platform
// that re-delivers the same id must not reset the clock — it would keep the row
// permanently due and turn one undelivered message into a tight retry loop.
func (s *Store) Retain(ctx context.Context, id, payload, firstSeenAt, nextAttemptAt,
	lastError string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: InboundPending, Key: pendingKey(id),
		Attrs: map[string]string{
			attrPayload:       payload,
			attrAttempts:      "0",
			attrFirstSeenAt:   firstSeenAt,
			attrNextAttemptAt: nextAttemptAt,
			attrLastError:     lastError,
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return fmt.Errorf("retain inbound %s: %w", id, err)
	}
	return nil
}

// DueForRedelivery answers which held messages are due at now, oldest first, at
// most limit of them.
//
// The limit bounds the HTTP calls one sweep pass makes, so it is applied AFTER
// the ordering rather than by the engine: the caller wants the limit oldest due
// messages, and a driver orders by the sort key, which here is the message id.
// The read therefore takes the whole due set and this method picks from it.
//
// That costs a read proportional to the backlog rather than to the limit, and it
// is the price of keeping the sort key on the id — the guarantee Retain rests
// on. It is bounded by drainage: the due set is empty whenever routd is up, and
// it holds only what arrived during an outage.
func (s *Store) DueForRedelivery(ctx context.Context, now string, limit int) ([]Held, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: InboundPending, Partition: pendingPartition,
		Where:   []objstore.Cmp{{Attr: attrNextAttemptAt, Op: objstore.Le, Value: now}},
		Project: []string{attrPayload, attrAttempts, attrFirstSeenAt},
	})
	if err != nil {
		return nil, fmt.Errorf("read the due inbound: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Attrs[attrFirstSeenAt] < items[j].Attrs[attrFirstSeenAt]
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	due := make([]Held, 0, len(items))
	for _, it := range items {
		attempts, err := strconv.Atoi(it.Attrs[attrAttempts])
		if err != nil {
			return nil, fmt.Errorf("read the due inbound: %s carries attempts %q: %w",
				it.Key.Sort, it.Attrs[attrAttempts], err)
		}
		due = append(due, Held{ID: it.Key.Sort, Payload: it.Attrs[attrPayload], Attempts: attempts})
	}
	return due, nil
}

// Reschedule records that an attempt failed and when the next one is due.
//
// attempts is the count the CALLER computed off the row it read, not an
// engine-side increment, because the sweep is the only writer of these three
// attributes and runs one pass at a time. A refusal therefore means the item is
// gone — something the sweep just read and only the sweep deletes — so it is
// reported rather than swallowed.
func (s *Store) Reschedule(ctx context.Context, id string, attempts int,
	nextAttemptAt, lastError string) error {
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: InboundPending, Key: pendingKey(id),
		Attrs: map[string]string{
			attrAttempts:      strconv.Itoa(attempts),
			attrNextAttemptAt: nextAttemptAt,
			attrLastError:     lastError,
		},
	}})
	if err != nil {
		return fmt.Errorf("back off inbound %s: %w", id, err)
	}
	if !committed {
		return fmt.Errorf("back off inbound %s: the sweep read it and it is no longer there, "+
			"so something outside the sweep is writing this queue", id)
	}
	return nil
}

// Drop stops holding a message: routd took it, or routd refused it in a way no
// retry repairs. The delete carries no condition, so calling it for a message
// already dropped is the no-op a delete means on every engine.
func (s *Store) Drop(ctx context.Context, id string) error {
	if _, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Delete, Entity: InboundPending, Key: pendingKey(id),
	}}); err != nil {
		return fmt.Errorf("drop inbound %s: %w", id, err)
	}
	return nil
}

// HeldCount reports how many inbound messages the adapter is holding
// undelivered. /health reports it so the backlog is visible where an operator
// already looks, rather than only in a WARN line nobody reads.
//
// It projects one small attribute rather than reading the documents: the answer
// is the number of items, and a health check must not pull every held payload
// through the driver to count them.
func (s *Store) HeldCount(ctx context.Context) (int, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: InboundPending, Partition: pendingPartition,
		Project: []string{attrAttempts},
	})
	if err != nil {
		return 0, fmt.Errorf("count the held inbound: %w", err)
	}
	return len(items), nil
}

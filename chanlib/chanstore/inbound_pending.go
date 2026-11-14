package chanstore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

const (
	attrPayload       = "payload"
	attrAttempts      = "attempts"
	attrFirstSeenAt   = "first_seen_at"
	attrNextAttemptAt = "next_attempt_at"
	attrLastError     = "last_error"
)

const pendingPartition = "inbound"

type Held struct {
	ID       string
	Payload  string
	Attempts int
}

func pendingKey(id string) objstore.Key {
	return objstore.Key{Partition: pendingPartition, Sort: id}
}

func (s *Store) Retain(ctx context.Context, id, payload, firstSeenAt, nextAttemptAt,
	lastError string) error {
	if err := s.ensureHeldCount(ctx); err != nil {
		return err
	}
	_, _, err := objstore.Increment(ctx, s.s, objstore.IndexEntity, heldCountKey, "count", 1, nil, []objstore.Write{{
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

func (s *Store) DueForRedelivery(ctx context.Context, now string, limit int) ([]Held, error) {
	refs, err := s.s.Query(ctx, objstore.Query{Entity: objstore.IndexEntity, Partition: "inbound/due",
		Key: objstore.KeyPredicate{Op: objstore.LtKey, Value: now + "\x7f"}})
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := strings.SplitN(refs[i].Key.Sort, "\x1f", 3)[1], strings.SplitN(refs[j].Key.Sort, "\x1f", 3)[1]
		if a == b {
			return refs[i].Attrs["source_sort"] < refs[j].Attrs["source_sort"]
		}
		return a < b
	})
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	items := make([]objstore.Item, 0, len(refs))
	for _, ref := range refs {
		it, ok, err := s.point(ctx, InboundPending, pendingKey(ref.Attrs["source_sort"]))
		if err != nil {
			return nil, err
		}
		if ok && it.Attrs[attrNextAttemptAt] <= now {
			items = append(items, it)
		}
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

func (s *Store) Drop(ctx context.Context, id string) error {
	if err := s.ensureHeldCount(ctx); err != nil {
		return err
	}
	_, _, err := objstore.Increment(ctx, s.s, objstore.IndexEntity, heldCountKey, "count", -1, nil, []objstore.Write{{
		Op: objstore.Delete, Entity: InboundPending, Key: pendingKey(id), Cond: objstore.Cond{Item: objstore.ItemPresent},
	}})
	return err
}

func (s *Store) HeldCount(ctx context.Context) (int, error) {
	it, ok, err := s.point(ctx, objstore.IndexEntity, heldCountKey)
	if err != nil || !ok {
		return 0, err
	}
	return strconv.Atoi(it.Attrs["count"])
}

var heldCountKey = objstore.Key{Partition: "inbound", Sort: "count"}

func (s *Store) ensureHeldCount(ctx context.Context) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Merge, Entity: objstore.IndexEntity,
		Key: heldCountKey, Init: map[string]string{"count": "0"}}})
	return err
}

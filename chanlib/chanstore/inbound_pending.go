package chanstore

// inbound_pending.go holds every read and write of `inbound_pending`: one item
// per inbound message routd did not accept, held by the adapter until it does.
//
// Only adapters that CONSUME the platform event on arrival keep these items.
// slakd answers Slack 200 before routd is called (Slack demands the ack inside
// 3s and redelivers on non-2xx, so deferring the ack turns one slow routd into
// several copies of the message); discd's gateway hands each event over once;
// gitd's webhook is delivered once. Neither platform will offer the message
// again, so a failed routd call would lose it with one ERROR line as the trace
// (BUGS L25).
//
// The id is the adapter's stable message id — the same value routd stores as the
// messages primary key, so a re-delivery of a row routd already has is a no-op
// rather than a duplicate.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

const (
	attrPayload       = "payload"
	attrAttempts      = "attempts"
	attrFirstSeenAt   = "first_seen_at"
	attrNextAttemptAt = "next_attempt_at"
	attrLastError     = "last_error"
	attrRetryState    = "retry_state"
)

// pendingPartition is the one partition every held message lives in: the
// database already IS one adapter's queue, so there is nothing to filter by.
// Accepted rows leave, so the steady state is empty (measured 2026-09-07: zero
// rows in both live teled databases) and the queue fills while routd is down.
// A permanently refused or exhausted row stays held with retry_state=failed: it
// has no due pointer and still counts as held.
//
// The sort key is the message id because id-uniqueness is the guarantee: a row
// already held keeps its attempt count and its backoff, enforced by a Put under
// ItemAbsent on this key. A time-valued key would let one id land twice — Slack
// retries a non-2xx ack — and be delivered twice.
const pendingPartition = "inbound"

type Held struct {
	ID       string
	Payload  string
	Attempts int
	Revision string
}

func retryAttrs(d retry.Decision, notice string) (map[string]string, []string) {
	attrs := map[string]string{attrAttempts: strconv.Itoa(d.Failures), attrLastError: notice, attrRetryState: "failed"}
	if d.Stop {
		return attrs, []string{attrNextAttemptAt}
	}
	attrs[attrRetryState] = "pending"
	attrs[attrNextAttemptAt] = core.TS(d.NextAt)
	return attrs, nil
}

func heldCond(h Held) objstore.Cond {
	revision := objstore.Cmp{Attr: objstore.IndexRevision, Op: objstore.Eq, Value: h.Revision}
	if h.Revision == "" {
		revision.Op = objstore.Absent
	}
	return objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{revision, {Attr: attrAttempts, Op: objstore.Eq, Value: strconv.Itoa(h.Attempts)}}}
}

func pendingKey(id string) objstore.Key {
	return objstore.Key{Partition: pendingPartition, Sort: id}
}

func parseInboundDecision(it objstore.Item) (retry.Decision, error) {
	attempts, err := strconv.Atoi(it.Attrs[attrAttempts])
	if err != nil || attempts < 0 {
		return retry.Decision{}, fmt.Errorf("invalid inbound %s attempts %q", it.Key.Sort, it.Attrs[attrAttempts])
	}
	if it.Attrs[attrLastError] == "" {
		return retry.Decision{}, fmt.Errorf("invalid inbound %s notice", it.Key.Sort)
	}
	d := retry.Decision{Failures: attempts}
	switch it.Attrs[attrRetryState] {
	case "failed":
		if _, ok := it.Attrs[attrNextAttemptAt]; ok {
			return d, fmt.Errorf("failed inbound %s has deadline", it.Key.Sort)
		}
		d.Stop = true
	case "pending":
		d.NextAt, err = time.Parse(time.RFC3339Nano, it.Attrs[attrNextAttemptAt])
		if err != nil {
			return d, fmt.Errorf("invalid inbound %s deadline: %w", it.Key.Sort, err)
		}
	default:
		return d, fmt.Errorf("invalid inbound %s state %q", it.Key.Sort, it.Attrs[attrRetryState])
	}
	return d, nil
}

func (s *Store) ReadInboundFailure(ctx context.Context, id string) (*retry.Error, error) {
	it, ok, err := s.point(ctx, InboundPending, pendingKey(id), attrAttempts, attrRetryState, attrNextAttemptAt, attrLastError)
	if err != nil || !ok {
		return nil, err
	}
	d, err := parseInboundDecision(it)
	if err != nil {
		return nil, err
	}
	failure := retry.Failure{Kind: "transient", RetryAt: d.NextAt}
	if d.Stop {
		failure.Kind = "permanent"
	}
	return &retry.Error{Failure: failure, Err: errors.New(it.Attrs[attrLastError])}, nil
}

func (s *Store) Retain(ctx context.Context, id, payload, firstSeenAt string, decision retry.Decision, notice string) error {
	attrs, _ := retryAttrs(decision, notice)
	attrs[attrPayload], attrs[attrFirstSeenAt] = payload, firstSeenAt
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: InboundPending, Key: pendingKey(id),
		Attrs: attrs,
		Cond:  objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return fmt.Errorf("retain inbound %s: %w", id, err)
	}
	return nil
}

func (s *Store) DueForRedelivery(ctx context.Context, now string, limit int) ([]Held, error) {
	at, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return nil, err
	}
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
	due := make([]Held, 0, len(refs))
	for _, ref := range refs {
		it, ok, err := s.point(ctx, InboundPending, pendingKey(ref.Attrs["source_sort"]))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		decision, err := parseInboundDecision(it)
		if err != nil {
			return nil, err
		}
		if !decision.IsDue(at) {
			continue
		}
		due = append(due, Held{ID: it.Key.Sort, Payload: it.Attrs[attrPayload], Attempts: decision.Failures, Revision: it.Attrs[objstore.IndexRevision]})
	}
	return due, nil
}

func (s *Store) Reschedule(ctx context.Context, observed Held, decision retry.Decision, notice string) error {
	attrs, remove := retryAttrs(decision, notice)
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: InboundPending, Key: pendingKey(observed.ID),
		Attrs: attrs, Remove: remove, Cond: heldCond(observed),
	}})
	if err != nil {
		return fmt.Errorf("back off inbound %s: %w", observed.ID, err)
	}
	return nil
}

func (s *Store) Drop(ctx context.Context, observed Held) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Delete, Entity: InboundPending, Key: pendingKey(observed.ID), Cond: heldCond(observed),
	}})
	return err
}

func (s *Store) HeldCount(ctx context.Context) (int, error) {
	rows, err := s.s.Query(ctx, objstore.Query{Entity: InboundPending, Partition: pendingPartition, Project: []string{attrAttempts}})
	return len(rows), err
}

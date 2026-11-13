package chanstore

// import.go writes an adapter's delivery claims and held inbound messages as
// items from plain values, and owns the item shape.
//
// NOTHING IN PRODUCTION CALLS IT. ClaimDelivery opens a claim at now and
// RecordDelivered settles it at now, so a claim already sent, at the instant a
// test chooses, is a state no live method reaches. A test writes one here and
// reads it back through the methods the adapter itself calls.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// A Snapshot is one adapter database as plain values.
type Snapshot struct {
	Claims []ClaimRow
	Held   []HeldRow
}

// ClaimRow is one outbound delivery the adapter was asked to make.
type ClaimRow struct {
	IdempotencyKey string
	ChatJID        string
	State          string
	PlatformID     string
	CreatedAt      string
	SettledAt      string
}

// HeldRow is one inbound message routd has not accepted.
type HeldRow struct {
	ID            string
	Payload       string
	Attempts      int
	FirstSeenAt   string
	NextAttemptAt string
	LastError     string
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source.
type Counts struct {
	Claims int
	Held   int
}

// Import writes snap into the entities. It is idempotent — every write replaces
// the item at its coordinate — so a run that failed part way is re-runnable.
// Run it with the adapter stopped: it rewrites what it is given, and a delivery
// the adapter recorded after the snapshot was read is not in the snapshot.
func (s *Store) Import(ctx context.Context, snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	// MaxBatchActions is the weakest engine's transaction bound, so the import
	// respects it too. The writes are independent replacements, so a chunk that
	// lands and a chunk that does not leave a partial import a re-run finishes.
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := s.s.GuardedBatch(ctx, nil, writes[:n])
		if err != nil {
			return Counts{}, err
		}
		if !committed {
			return Counts{}, fmt.Errorf("the import batch was refused; nothing in it conditions "+
				"on anything, so the store rejected %d unconditional writes", n)
		}
		writes = writes[n:]
	}
	return counts, nil
}

// CheckSnapshot reports whether the entities can represent snap, writing
// nothing. It is what a rehearsal runs: a row the store cannot address should be
// named before the run that would stop half way through, not during it.
func CheckSnapshot(snap Snapshot) (Counts, error) {
	_, counts, err := importWrites(snap)
	return counts, err
}

// importWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes  []objstore.Write
		counts  Counts
		claimed = map[string]bool{}
		held    = map[string]bool{}
		refused []string
	)
	for _, c := range snap.Claims {
		switch {
		case c.IdempotencyKey == "":
			// The key IS the partition, and a query without a partition is
			// refused by every driver, so a blank key writes an item nothing can
			// ever read back.
			refused = append(refused,
				fmt.Sprintf("a claim row has no idempotency_key (for chat %q)", c.ChatJID))
			continue
		case claimed[c.IdempotencyKey]:
			// Two rows at one key are one item, and the second would blow the
			// batch up mid-import rather than here.
			refused = append(refused, fmt.Sprintf("key %s appears twice", c.IdempotencyKey))
			continue
		case c.State != stateInflight && c.State != stateSent:
			// A state no reader understands is worse than a missing row: an
			// unknown value reads as "not sent", so the next attempt on the key
			// is refused as unresolved forever.
			refused = append(refused, fmt.Sprintf("key %s is in state %q, which is neither %s "+
				"nor %s", c.IdempotencyKey, c.State, stateInflight, stateSent))
			continue
		}
		claimed[c.IdempotencyKey] = true
		// An empty column becomes an ABSENT attribute rather than an empty
		// value, because absent is the only null this model has.
		attrs := map[string]string{attrChatJID: c.ChatJID, attrState: c.State}
		for name, value := range map[string]string{
			attrPlatformID: c.PlatformID, attrCreatedAt: c.CreatedAt, attrSettledAt: c.SettledAt,
		} {
			if value != "" {
				attrs[name] = value
			}
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: OutboundDedup, Key: dedupKey(c.IdempotencyKey), Attrs: attrs,
		})
		counts.Claims++
	}
	for _, h := range snap.Held {
		switch {
		case h.ID == "":
			refused = append(refused, "a held row has no id, and the sweep addresses it by id")
			continue
		case held[h.ID]:
			refused = append(refused, fmt.Sprintf("held message %s appears twice", h.ID))
			continue
		case len(h.FirstSeenAt) != tsWidth || len(h.NextAttemptAt) != tsWidth:
			// The sweep compares both by byte order, so a value of another width
			// sorts into the wrong place instead of failing (BUGS L97). Every
			// driver refuses it; naming it here says WHICH row.
			refused = append(refused, fmt.Sprintf("held message %s carries first_seen_at %q and "+
				"next_attempt_at %q, and the sweep compares both at %d bytes",
				h.ID, h.FirstSeenAt, h.NextAttemptAt, tsWidth))
			continue
		}
		held[h.ID] = true
		attrs := map[string]string{
			attrPayload:       h.Payload,
			attrAttempts:      strconv.Itoa(h.Attempts),
			attrFirstSeenAt:   h.FirstSeenAt,
			attrNextAttemptAt: h.NextAttemptAt,
		}
		if h.LastError != "" {
			attrs[attrLastError] = h.LastError
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: InboundPending, Key: pendingKey(h.ID), Attrs: attrs,
		})
		counts.Held++
	}
	// Every refusal is reported at once: an operator deciding what to do about
	// the data needs the whole picture, not the first row the loop met.
	if len(refused) > 0 {
		return nil, Counts{}, fmt.Errorf("%d rows cannot be represented:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}

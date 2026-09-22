package chanstore

// outbound_dedup.go holds every read and write of `outbound_dedup`: one item per
// outbound delivery the adapter was asked to make, addressed by routd's
// idempotency key.
//
// routd re-sends a row that stays `pending` (its 30s sweep), so the adapter
// receives the SAME key more than once by design. Without a record the second
// delivery posts a second platform message. The record is written BEFORE the
// platform call, so a lost response leaves `inflight` — an outcome the adapter
// cannot resolve and must not guess (BUGS D5).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes an item carries. `state` and `platform_id` are what a second
// claim reads back; `chat_jid`, `created_at` and `settled_at` are the operator's
// evidence for a delivery that went wrong, and nothing selects on them.
const (
	attrChatJID    = "chat_jid"
	attrState      = "state"
	attrPlatformID = "platform_id"
	attrCreatedAt  = "created_at"
	attrSettledAt  = "settled_at"
)

// The two states a claim can be in.
//
// It is an explicit attribute rather than a tombstone read off `settled_at` or
// `platform_id`: an adapter verb may hand back an empty platform id, and an
// empty string is PRESENT, so inferring the state from another attribute's
// presence would answer "sent" for a claim that never settled. The legacy rows
// carry the column too, so the move copies it rather than deriving it.
const (
	stateInflight = "inflight"
	stateSent     = "sent"
)

// dedupKey is the coordinate of one claim.
//
// THE PARTITION IS THE IDEMPOTENCY KEY, not a constant naming the adapter. A
// partition is the coordinate a caller FILTERS on (6/37 rule 1) and no read here
// filters: all three touch exactly one key, handed to them by routd. So no read
// wants two claims co-located and nothing is paid for spreading them.
//
// What a constant partition would cost is growth. A settled claim is never
// deleted and nothing sweeps this entity — measured 2026-09-07 on krons, 34 and
// 76 rows in the two live teled databases, both entirely `sent` — so
// `const Partition = "chanlib"` would pile an unbounded ledger into one
// partition for no read's benefit. That is authd's mistake (6/37 step 3, "a
// single constant partition … every read is a point read or a whole-partition
// scan") without even authd's excuse, because this ledger has no bound.
//
// The sort key repeats the key because the item has no second coordinate: there
// is exactly one item per idempotency key, and DynamoDB refuses an empty key
// attribute, so a blank sort key is not a key on every engine.
func dedupKey(key string) objstore.Key { return objstore.Key{Partition: key, Sort: key} }

// ClaimState is what a claim on an idempotency key found.
type ClaimState uint8

const (
	// ClaimWon means nothing held the key and this caller now does. It is the
	// only state that permits a platform call.
	ClaimWon ClaimState = iota
	// ClaimSent means the key was already delivered; PlatformID is the id the
	// platform gave for it.
	ClaimSent
	// ClaimInflight means an earlier attempt started the platform call and never
	// learned whether it landed.
	ClaimInflight
)

// Claim is the answer ClaimDelivery gives.
type Claim struct {
	State      ClaimState
	PlatformID string
}

// ClaimDelivery records the intent to deliver key and reports whether the caller
// may proceed.
//
// THE CLAIM IS WHAT MAKES A DELIVERY EXACTLY-ONCE, and the whole guarantee rests
// on one condition: the write is a Put under ItemAbsent, so the ENGINE's own
// uniqueness decides who won, never a read this package performed. Drop that
// condition and the put becomes an upsert every caller commits, every claim
// looks fresh, and routd's re-send of a still-`pending` row posts the message a
// second time.
//
// A refused claim is a normal outcome, not a fault: somebody holds the key, and
// the point read that follows says who and in what state.
func (s *Store) ClaimDelivery(ctx context.Context, key, jid string) (Claim, error) {
	won, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: OutboundDedup, Key: dedupKey(key),
		Attrs: map[string]string{
			attrChatJID:   jid,
			attrState:     stateInflight,
			attrCreatedAt: core.NowTS(),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return Claim{}, fmt.Errorf("claim delivery %s: %w", key, err)
	}
	if won {
		return Claim{State: ClaimWon}, nil
	}
	claim, held, err := s.RecordedClaim(ctx, key)
	if err != nil {
		return Claim{}, err
	}
	if !held {
		// The claim was refused because the item exists, so a read that misses
		// means something deleted it in between. Nothing in chanlib deletes a
		// claim it does not hold, so this is the store disagreeing with itself.
		return Claim{}, fmt.Errorf("read delivery claim %s: the put was refused because the "+
			"item is there and the read did not find it", key)
	}
	return claim, nil
}

// RecordedClaim answers what a key's record holds, writing nothing.
//
// It is the read ClaimDelivery performs after a refused claim, and the two share
// it rather than each having their own — a second reader of the same item that
// classified `state` differently is exactly the drift that would let one path
// call a message unsent and the other call it delivered.
//
// The caller that needs it WITHOUT the claim verifies what landed: reading a
// key back through ClaimDelivery would CREATE the record for any row that never
// arrived, so the check would repair its own evidence.
func (s *Store) RecordedClaim(ctx context.Context, key string) (Claim, bool, error) {
	it, held, err := s.point(ctx, OutboundDedup, dedupKey(key), attrState, attrPlatformID)
	if err != nil {
		return Claim{}, false, fmt.Errorf("read delivery claim %s: %w", key, err)
	}
	if !held {
		return Claim{}, false, nil
	}
	if it.Attrs[attrState] == stateSent {
		return Claim{State: ClaimSent, PlatformID: it.Attrs[attrPlatformID]}, true, nil
	}
	return Claim{State: ClaimInflight}, true, nil
}

// RecordDelivered records that the platform accepted the message.
//
// The error is RETURNED rather than logged here: the message IS delivered by the
// time this runs, so whether a failed record should fail the send is the
// caller's policy, not the store's (dedup.go keeps it non-fatal and loud).
func (s *Store) RecordDelivered(ctx context.Context, key, platformID string) error {
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: OutboundDedup, Key: dedupKey(key),
		Attrs: map[string]string{
			attrState:      stateSent,
			attrPlatformID: platformID,
			attrSettledAt:  core.NowTS(),
		},
	}})
	if err != nil {
		return fmt.Errorf("record delivery %s: %w", key, err)
	}
	if !committed {
		// An Update always requires the item, so a refusal is a missing claim —
		// and this call runs on the path that just created one. Silence here
		// would leave the next attempt on this key free to post the message
		// again, which is the duplicate the claim exists to stop.
		return fmt.Errorf("record delivery %s: the claim is gone, so a re-send of this key "+
			"would post the message a second time", key)
	}
	return nil
}

// ReleaseUnsentClaim drops the claim after a platform call that definitively did
// NOT deliver. A refused call left no platform message, so the key is free for
// routd's retry; keeping the claim would refuse every later attempt.
//
// ONLY AN INFLIGHT CLAIM IS RELEASED, and that condition rides on the delete
// rather than on a read before it. A claim that has settled names a message the
// platform is holding, and deleting its record would let the next attempt post
// that message again. A refusal is therefore the correct no-op, which is what
// the old `AND state='inflight'` said by matching no row.
func (s *Store) ReleaseUnsentClaim(ctx context.Context, key string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Delete, Entity: OutboundDedup, Key: dedupKey(key),
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrState, Op: objstore.Eq, Value: stateInflight},
		}},
	}})
	if err != nil {
		return fmt.Errorf("release the unsent claim %s: %w", key, err)
	}
	return nil
}

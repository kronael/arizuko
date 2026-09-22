package store

// invite_redemptions.go holds every read and write of `invite_redemptions`: one
// item per subgroup invite a person has followed and not yet spent, carrying the
// parent subtree the operator invited them into.
//
// It is the authority handleCreateWorld derives a parent folder from: an item
// the redemption writes is not forgeable by the redeemer, where a
// `pending_target` cookie was (BUGS F50).
//
// The partition is the person and the sort key the instant: every operation
// names the sub, so "my most recent unspent one" is a descending range of ONE
// item. The instant IS the identity, core.TS plus a suffix that keeps two
// redemptions at one nanosecond apart, and `redeemed_at` is not stored again.
// core.TS rather than onbod's twenty-byte instants because this one is
// COMPARED (BUGS L97).
//
// Spending is a claim: the delete's own refusal is the answer, so two
// create-world posts that read one redemption cannot both spend it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// attrRedemptionGlob is the parent subtree the operator invited this person
// into: the only attribute an item carries.
const attrRedemptionGlob = "target_glob"

// redemptionSuffixHex is how many hex digits separate two redemptions recorded
// in the same nanosecond.
const redemptionSuffixHex = 8

// RedemptionStampWidth is the width of a redemption's position. Exported
// because the move that carries these rows builds one from a legacy row's id.
const RedemptionStampWidth = tsWidth + 1 + redemptionSuffixHex

// Redemption is one unspent subgroup-invite redemption. `at` is unexported: a
// caller that could name one could spend a redemption it never read, and the
// spend is the authority a world is created under.
type Redemption struct {
	TargetGlob string
	at         string
}

// RedemptionStamp assembles a position from a rendered instant and a suffix, so
// the live writer and the move build one shape.
func RedemptionStamp(ts, suffix string) string { return ts + "-" + suffix }

// redemptionNowStamp is the position a redemption recorded now takes.
func redemptionNowStamp() (string, error) {
	raw := make([]byte, redemptionSuffixHex/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("redemption position: %w", err)
	}
	return RedemptionStamp(core.NowTS(), hex.EncodeToString(raw)), nil
}

// redemptionKey is the coordinate of one redemption.
func redemptionKey(userSub, at string) (objstore.Key, error) {
	if userSub == "" {
		return objstore.Key{}, fmt.Errorf("invite redemption carries no user_sub, which is " +
			"half its coordinate")
	}
	if len(at) != RedemptionStampWidth {
		return objstore.Key{}, fmt.Errorf("invite redemption position %q is %d bytes and a "+
			"position is %d; a person's redemptions are read in position order, so a short "+
			"one sorts into the wrong place instead of failing (BUGS L97)",
			at, len(at), RedemptionStampWidth)
	}
	return objstore.Key{Partition: userSub, Sort: at}, nil
}

// LatestRedemption answers a person's most recent unspent subgroup-invite
// redemption, and whether they hold one. Most recent, because a caller holding
// two is answering the invite they just followed. It FAILS CLOSED: an
// unreadable partition is no authority.
func (s *Store) LatestRedemption(ctx context.Context, userSub string) (Redemption, bool, error) {
	if userSub == "" {
		return Redemption{}, false, nil
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: InviteRedemptions, Partition: userSub,
		Descending: true, Limit: 1,
	})
	if err != nil {
		return Redemption{}, false, fmt.Errorf("read %s's invite redemptions: %w", userSub, err)
	}
	if len(items) == 0 {
		return Redemption{}, false, nil
	}
	return Redemption{
		TargetGlob: items[0].Attrs[attrRedemptionGlob],
		at:         items[0].Key.Sort,
	}, true, nil
}

// SpendRedemption consumes the redemption a read produced, and answers whether
// THIS call is the one that spent it: a replay (a resubmitted form, a second
// tab) finds nothing to derive a parent from and creates no second world.
func (s *Store) SpendRedemption(ctx context.Context, userSub string, r Redemption) (bool, error) {
	key, err := redemptionKey(userSub, r.at)
	if err != nil {
		return false, err
	}
	spent, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Delete, Entity: InviteRedemptions, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
	if err != nil {
		return false, fmt.Errorf("spend %s's redemption: %w", userSub, err)
	}
	return spent, nil
}

// CountRedemptions answers how many unspent redemptions one person holds.
func (s *Store) CountRedemptions(ctx context.Context, userSub string) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: InviteRedemptions, Partition: userSub,
	})
	if err != nil {
		return 0, fmt.Errorf("read %s's invite redemptions: %w", userSub, err)
	}
	return len(items), nil
}

// redemptionRowPut renders one legacy row as the item it becomes. `redeemed_at`
// is second-precision, so the row number is what keeps two redemptions of one
// second in order.
func redemptionRowPut(r RedemptionRow) (objstore.Write, error) {
	at, err := normaliseInstant(InviteRedemptions, "redeemed_at", r.RedeemedAt)
	if err != nil {
		return objstore.Write{}, err
	}
	if r.RowID < 0 {
		return objstore.Write{}, fmt.Errorf("invite redemption id %d is not a row number, "+
			"and it is what keeps two redemptions of one second apart", r.RowID)
	}
	return redemptionWrite(r.UserSub, RedemptionStamp(at, fmt.Sprintf("%08x", r.RowID)),
		r.TargetGlob)
}

// redemptionWrite renders the item a consumed subgroup invite records, so the
// live path and the move build one shape.
func redemptionWrite(userSub, at, targetGlob string) (objstore.Write, error) {
	key, err := redemptionKey(userSub, at)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrRedemptionGlob, targetGlob)
	return objstore.Write{
		Op: objstore.Put, Entity: InviteRedemptions, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, nil
}

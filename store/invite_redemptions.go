package store

// invite_redemptions.go holds every read and write of `invite_redemptions`: one
// item per subgroup invite a person has followed and not yet spent, carrying the
// parent subtree the operator invited them into.
//
// IT IS THE AUTHORITY handleCreateWorld DERIVES A PARENT FOLDER FROM, and that
// is why it exists at all: an item the redemption writes is not forgeable by the
// redeemer, where a `pending_target` cookie was — `Cookie:
// pending_target=victim/` created victim/<name> and took admin over it for any
// authenticated caller holding no grant anywhere (BUGS F50).
//
// THE PARTITION IS THE PERSON AND THE SORT KEY IS THE INSTANT. Every one of the
// three operations names the sub — the write, the "my most recent unspent one"
// read, and the delete that spends it — so the person is the tenancy coordinate
// rule 1 asks for, and with the instant sorting inside it the read is a
// descending range of ONE item rather than a scan with a maximum taken in the
// reader. It is the one entity in this package whose partition is not an owner,
// for the reason runed's session log's is a folder: the question is always asked
// about one of them.
//
// THE INSTANT IS THE IDENTITY, which is rule 4. The statement this replaces read
// `ORDER BY id DESC LIMIT 1` over an AUTOINCREMENT id, and no document store
// mints one — and the id never left the process: `latestRedemption` produced it
// and `spendRedemption` consumed it, so nothing outside that pair could name a
// row. The position is core.TS at its fixed width plus the suffix that keeps two
// redemptions at one nanosecond apart, and `redeemed_at` is not stored again.
//
// It is core.TS rather than the twenty-byte instant onbod's other entities carry
// because this one is COMPARED: those are read back and rendered, this one
// decides which of a person's redemptions is the most recent, and a comparison
// on a stored instant is a byte comparison (BUGS L97).
//
// SPENDING IS A CLAIM. The delete's own refusal is the answer, so two
// create-world posts that read one redemption cannot both spend it — one invite
// redemption is one world. That is what `RowsAffected` said, and it is the whole
// concurrency control on a path where the read and the delete are forty lines
// apart.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// attrRedemptionGlob is the parent subtree the operator invited this person
// into. It is the only attribute an item carries.
const attrRedemptionGlob = "target_glob"

// redemptionSuffixHex is how many hex digits separate two redemptions recorded
// in the same nanosecond. Four bytes of randomness, for audit_log's reason.
const redemptionSuffixHex = 8

// RedemptionStampWidth is the width of a redemption's position: core.TS's thirty
// bytes, a separator, and the suffix. It is exported because the move that
// carries these rows builds one from a legacy row's id.
const RedemptionStampWidth = tsWidth + 1 + redemptionSuffixHex

// Redemption is one unspent subgroup-invite redemption.
//
// `at` is unexported for the reason a queued system event's position is: a
// caller that could name one could spend a redemption it never read, and the
// spend is the authority a world is created under.
type Redemption struct {
	TargetGlob string
	at         string
}

// RedemptionStamp assembles a position from a rendered instant and a suffix, so
// the live writer and the move that carries an existing row build one shape.
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
// redemption, and whether they hold one.
//
// Most recent because a caller holding two is answering the invite they just
// followed; each is spent on its own, so two invites still yield two worlds.
//
// It FAILS CLOSED: an unreadable partition is no authority, so the caller is
// told there is no redemption rather than being handed one it could not check.
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
// THIS call is the one that spent it.
//
// The delete's own refusal is the answer, so a replay of the same redemption — a
// resubmitted form, a second tab — finds nothing to derive a parent from and
// creates no second world.
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
// `arizuko migrate-onbod` reads it back to check that every row travelled.
func (s *Store) CountRedemptions(ctx context.Context, userSub string) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: InviteRedemptions, Partition: userSub,
	})
	if err != nil {
		return 0, fmt.Errorf("read %s's invite redemptions: %w", userSub, err)
	}
	return len(items), nil
}

// redemptionRowPut renders one legacy row as the item it becomes. The position
// is the instant it was redeemed at plus the row number that broke a tie:
// `redeemed_at` is second-precision, so the id is what keeps two redemptions of
// one second in the order the read answered them in.
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
// live path and the move that carries an existing row build one shape.
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

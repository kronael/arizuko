package store

// import.go places onbod's three tables as items from plain values, and owns
// the item shape.
//
// onbod's four instants are COPIED rather than re-rendered: every value in the
// fleet is exactly 20 bytes (measured 2026-09-07), and widening them would move
// every checksum for nothing. A wrong width is REFUSED by the driver's own
// validator before any write lands.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// OnbodSnapshot is the legacy `onboarding`, `onboarding_gates` and `invites`
// tables as the source reads them, as this package's own plain values.
type OnbodSnapshot struct {
	Admissions  []OnboardingRow
	Gates       []OnboardingGate
	Invites     []ArchiveInviteRow
	Redemptions []RedemptionRow
}

// RedemptionRow is one legacy `invite_redemptions` row, carrying the rowid its
// position's suffix is built from.
type RedemptionRow struct {
	RowID      int64
	UserSub    string
	TargetGlob string
	RedeemedAt string
}

// OnbodCounts is what an import wrote, per entity, for the operator to check
// against the source. A ZERO IS NOT AN ALL-CLEAR: `onboarding_gates` is empty
// on all three live instances (2026-09-07), so no gates is correct there and
// the FIRST symptom of a tool pointed at the wrong database anywhere else.
type OnbodCounts struct {
	Admissions  int
	Gates       int
	Invites     int
	Redemptions int
}

// CheckOnbodSnapshot reports whether the entities can represent snap, writing
// nothing: a row the store cannot address is named before the run that would
// stop half way through.
func CheckOnbodSnapshot(snap OnbodSnapshot) (OnbodCounts, error) {
	_, counts, err := onbodWrites(snap)
	return counts, err
}

// ImportOnbod writes snap into the entities and answers what landed. The
// writes are UNCONDITIONAL, for Import's reason. ImportOnboarding and
// ImportInvites, the archive lane, deliberately do NOT overwrite: a jid the
// target already holds is a live admission a restore must not walk backwards.
func (s *Store) ImportOnbod(ctx context.Context, snap OnbodSnapshot) (OnbodCounts, error) {
	writes, counts, err := onbodWrites(snap)
	if err != nil {
		return OnbodCounts{}, err
	}
	if _, err := s.applyInChunks(ctx, "import onbod's tables", writes); err != nil {
		return OnbodCounts{}, err
	}
	return counts, nil
}

// onbodWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented. Every refusal is collected and reported at once.
func onbodWrites(snap OnbodSnapshot) ([]objstore.Write, OnbodCounts, error) {
	var (
		writes  []objstore.Write
		counts  OnbodCounts
		refused []string
	)
	// One claimed-coordinate set per entity: the three share a partition.
	claimed := map[string]map[string]bool{
		Onboarding: {}, OnboardingGates: {}, Invites: {}, InviteRedemptions: {},
	}
	take := func(entity, sort, what string) bool {
		if claimed[entity][sort] {
			refused = append(refused, fmt.Sprintf("two %s rows carry %s", what, sort))
			return false
		}
		claimed[entity][sort] = true
		return true
	}
	add := func(w objstore.Write, err error) bool {
		if err != nil {
			refused = append(refused, err.Error())
			return false
		}
		// The width rule is the DRIVER's, so it is checked with the driver's
		// own validator rather than a second copy of the widths (BUGS L97).
		if err := objstore.ValidateBatch(nil, []objstore.Write{w}); err != nil {
			refused = append(refused, err.Error())
			return false
		}
		writes = append(writes, w)
		return true
	}
	for _, r := range snap.Admissions {
		if r.JID != "" && !take(Onboarding, r.JID, "onboarding") {
			continue
		}
		if add(onboardingPut(r)) {
			counts.Admissions++
		}
	}
	for _, g := range snap.Gates {
		if g.Gate != "" && !take(OnboardingGates, g.Gate, "gate") {
			continue
		}
		if add(gatePut(g)) {
			counts.Gates++
		}
	}
	for _, inv := range snap.Invites {
		if inv.Ref != "" && !take(Invites, inv.Ref, "invite") {
			continue
		}
		if add(archiveInvitePut(inv)) {
			counts.Invites++
		}
	}
	for _, r := range snap.Redemptions {
		w, err := redemptionRowPut(r)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		// The person is the PARTITION, so only a repeat inside one partition
		// is a collision.
		if !take(InviteRedemptions, w.Key.Partition+KeySep+w.Key.Sort, "redemption") {
			continue
		}
		if add(w, nil) {
			counts.Redemptions++
		}
	}
	if len(refused) > 0 {
		return nil, OnbodCounts{}, fmt.Errorf("%d rows the entities cannot address:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}

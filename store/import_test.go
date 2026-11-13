package store

// import.go, exercised on the entities alone so the bodies run on BOTH engines
// — a writer that only works on the engine it was written against is the drift
// the conformance suite exists to catch (entities_test.go says why the subject
// is openEntities).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// The three sets land, and they land where their readers look — read back
// through the very methods onbod calls, so an item written at a coordinate the
// engine does not read would fail here rather than on a live instance.
func TestImportOnbodLandsWhereTheReadersLook(t *testing.T) {
	s := openEntities(t)
	snap := OnbodSnapshot{
		Admissions: []OnboardingRow{
			{JID: "telegram:user/1", Status: statusAwaitingMessage, Created: "2026-01-01T00:00:00Z"},
			{
				JID: "telegram:user/2", Status: statusQueued, UserSub: "github:alice",
				Gate: "github:org=co", Created: "2026-01-02T00:00:00Z",
				PromptedAt: "2026-01-02T00:01:00Z", QueuedAt: "2026-01-02T00:02:00Z",
			},
		},
		Gates: []OnboardingGate{{Gate: "github:org=co", LimitPerDay: 10, Enabled: true}},
		Invites: []ArchiveInviteRow{{
			Ref: "ref1", TargetGlob: "atlas/**", IssuedBySub: "github:alice",
			IssuedAt: "2026-01-01T00:00:00Z", MaxUses: 3, UsedCount: 1,
		}},
	}
	counts, err := CheckOnbodSnapshot(snap)
	if err != nil {
		t.Fatalf("the rehearsal refused a snapshot the entities can hold: %v", err)
	}
	if counts != (OnbodCounts{Admissions: 2, Gates: 1, Invites: 1}) {
		t.Errorf("rehearsal counted %+v", counts)
	}

	got, err := s.ImportOnbod(t.Context(), snap)
	if err != nil {
		t.Fatalf("ImportOnbod: %v", err)
	}
	if got != counts {
		t.Errorf("import wrote %+v, the rehearsal promised %+v", got, counts)
	}

	rows, err := s.ListOnboarding("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].JID != "telegram:user/2" || rows[0].Gate != "github:org=co" {
		t.Errorf("admissions = %+v", rows)
	}
	// The optional attributes an admission never reached stay ABSENT, so the
	// re-greet poll still sees the first row as never greeted.
	if rows[1].PromptedAt != "" || rows[1].UserSub != "" {
		t.Errorf("a pending admission came back with a verdict: %+v", rows[1])
	}
	gates, err := s.EnabledGates()
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 1 || gates[0].LimitPerDay != 10 {
		t.Errorf("gates = %+v", gates)
	}
	invs, err := s.ListInvites("")
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || invs[0].Ref != "ref1" || invs[0].MaxUses != 3 || invs[0].UsedCount != 1 {
		t.Errorf("invites = %+v", invs)
	}

	// Re-runnable: the same source writes the same items, so a retry after a
	// partial run is a retry and not a failure.
	if _, err := s.ImportOnbod(t.Context(), snap); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if n, _ := s.CountOnboarding(t.Context()); n != 2 {
		t.Errorf("re-import duplicated admissions: %d", n)
	}
}

// The rehearsal names every row it cannot address AT ONCE, because an operator
// deciding what to do about the data needs the whole picture. Three shapes are
// refused: a row with no coordinate, two rows at one coordinate, and an instant
// of the wrong width — the last is the driver's own rule, and a value that
// sorted into the wrong place would fail silently instead (BUGS L97).
func TestCheckOnbodSnapshotNamesEveryUnaddressableRow(t *testing.T) {
	_, err := CheckOnbodSnapshot(OnbodSnapshot{
		Admissions: []OnboardingRow{
			{JID: "", Status: statusAwaitingMessage, Created: "2026-01-01T00:00:00Z"},
			{JID: "telegram:1", Status: statusAwaitingMessage, Created: "2026-01-01T00:00:00Z"},
			{JID: "telegram:1", Status: statusQueued, Created: "2026-01-01T00:00:00Z"},
			{JID: "telegram:2", Status: statusAwaitingMessage, Created: "2026-01-01"},
		},
		Gates:   []OnboardingGate{{Gate: "", LimitPerDay: 1}},
		Invites: []ArchiveInviteRow{{Ref: "", TargetGlob: "atlas"}},
	})
	if err == nil {
		t.Fatal("the rehearsal accepted four rows the entities cannot address")
	}
	for _, want := range []string{
		"onboarding row carries no jid",
		"two onboarding rows carry telegram:1",
		"declared ordered width is 20",
		"gate row carries no gate",
		"invite carries no ref",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%v", want, err)
		}
	}
}

// A run that reports zero of an entity is the shape of a fixture pointed at the
// wrong database, and it is ALSO the correct answer for gates on every live
// instance. Nothing here can tell the two apart, which is why the count is
// reported rather than checked.
func TestImportOnbodAcceptsAnEmptySet(t *testing.T) {
	s := openEntities(t)
	counts, err := s.ImportOnbod(t.Context(), OnbodSnapshot{})
	if err != nil {
		t.Fatalf("ImportOnbod on an empty snapshot: %v", err)
	}
	if counts != (OnbodCounts{}) {
		t.Errorf("counts = %+v, want every entity at zero", counts)
	}
}

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
// the FIRST symptom of a fixture pointed at the wrong database anywhere else.
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
		r.TargetGlob, "")
}

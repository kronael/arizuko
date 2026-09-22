package store

// import.go, exercised on the entities alone so the bodies run on BOTH engines
// — a writer that only works on the engine it was written against is the drift
// the conformance suite exists to catch (entities_test.go says why the subject
// is openEntities).

import (
	"strings"
	"testing"
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

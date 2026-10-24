package store

// InsertOnboarding is both the row's creator and, since the fold (spec 5/31-identity-pairing
// § Deleting "greet once, ever"), the re-greet trigger. It fires from routd's
// route-MISS branch, so every call means "this chat spoke and still routes
// nowhere" — the only fact that justifies handing out another link.
//
// The assertions below read the stored ITEM rather than a row, because the
// distinction the whole re-greet turns on is absent-versus-empty: `prompted_at`
// missing means "never greeted", and an empty string sitting in its place is a
// value the poll reads as already prompted.
//
// The subject is openEntities, not OpenMem: an entity needs a driver and a
// declaration and nothing else, so these bodies run on BOTH engines, where the
// frozen chain OpenMem builds is refused by Postgres before any of this code
// runs (entities_test.go).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// admissionItem reads one admission exactly as it is stored.
func admissionItem(t *testing.T, s *Store, jid string) objstore.Item {
	t.Helper()
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Onboarding, Partition: OwnerOnbod,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: jid},
	})
	if err != nil {
		t.Fatalf("read onboarding item %s: %v", jid, err)
	}
	if len(items) != 1 {
		t.Fatalf("read onboarding item %s: %d items, want 1", jid, len(items))
	}
	return items[0]
}

// setAdmission merges attributes into one admission, for a fixture that has to
// build a state no accessor writes in one step.
func setAdmission(t *testing.T, s *Store, jid string, attrs map[string]string) {
	t.Helper()
	committed, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: Onboarding,
		Key:   objstore.Key{Partition: OwnerOnbod, Sort: jid},
		Attrs: attrs,
	}})
	if err != nil {
		t.Fatalf("set %s: %v", jid, err)
	}
	if !committed {
		t.Fatalf("set %s: refused, so there is no such admission", jid)
	}
}

// setPrompted stamps a greeting `ago` in the past and answers what it wrote.
func setPrompted(t *testing.T, s *Store, jid string, ago time.Duration) string {
	t.Helper()
	ts := onbodTS(time.Now().Add(-ago))
	setAdmission(t, s, jid, map[string]string{attrOnbPromptedAt: ts})
	return ts
}

func TestInsertOnboarding_CreatesAnUnpromptedRow(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	it := admissionItem(t, s, "telegram:1")
	if got := it.Attrs[attrOnbStatus]; got != statusAwaitingMessage {
		t.Errorf("status = %q, want %q", got, statusAwaitingMessage)
	}
	if got, ok := it.Attrs[attrOnbPromptedAt]; ok {
		t.Errorf("prompted_at = %q, want the attribute absent", got)
	}
}

// The permanent lockout blocker 2 named: a link that expired left the user with
// no way to ask for another, because the jid coordinate blocks a fresh item and
// the greeter only ever claims an admission with no prompted_at. A miss after
// PairingTTL now re-arms it.
func TestInsertOnboarding_ReGreetsAfterPairingTTL(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	setPrompted(t, s, "telegram:1", PairingTTL+time.Minute)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	if got, ok := admissionItem(t, s, "telegram:1").Attrs[attrOnbPromptedAt]; ok {
		t.Errorf("prompted_at = %q after a miss past the cooldown; want the attribute "+
			"REMOVED (re-armed)", got)
	}
}

// Within the cooldown the live link is still redeemable, so a second one would
// be pure noise. This is the spam bound: one live link at a time.
func TestInsertOnboarding_DoesNotReGreetWithinPairingTTL(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	stamped := setPrompted(t, s, "telegram:1", PairingTTL/2)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	got, ok := admissionItem(t, s, "telegram:1").Attrs[attrOnbPromptedAt]
	if !ok || got != stamped {
		t.Errorf("prompted_at = (%q, present=%v), want it untouched at %q", got, ok, stamped)
	}
}

// A row that already reached an admission verdict is not a greeting candidate.
// Re-arming it would greet a user who has been queued, approved, or refused —
// and, worse, hand a fresh pairing link to a REFUSED identity.
func TestInsertOnboarding_NeverReArmsAnAdmittedRow(t *testing.T) {
	s := openEntities(t)

	for _, status := range []string{statusQueued, statusApproved, statusRefused} {
		jid := "telegram:" + status
		if err := s.InsertOnboarding(jid, jid); err != nil {
			t.Fatal(err)
		}
		stamped := setPrompted(t, s, jid, PairingTTL+time.Hour)
		setAdmission(t, s, jid, map[string]string{
			attrOnbStatus:  status,
			attrOnbUserSub: "github:alice",
		})

		if err := s.InsertOnboarding(jid, jid); err != nil {
			t.Fatal(err)
		}
		it := admissionItem(t, s, jid)
		if got := it.Attrs[attrOnbStatus]; got != status {
			t.Errorf("%s row moved to %q", status, got)
		}
		got, ok := it.Attrs[attrOnbPromptedAt]
		if !ok || got != stamped {
			t.Errorf("%s row re-armed: prompted_at %q -> (%q, present=%v)",
				status, stamped, got, ok)
		}
	}
}

// The re-greet must not resurrect the row's other facts. `created` is when the
// chat first appeared, not when it last spoke.
func TestInsertOnboarding_ReGreetKeepsCreated(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	created := admissionItem(t, s, "telegram:1").Attrs[attrOnbCreated]
	setPrompted(t, s, "telegram:1", PairingTTL+time.Minute)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	if after := admissionItem(t, s, "telegram:1").Attrs[attrOnbCreated]; after != created {
		t.Errorf("created moved: %q -> %q", created, after)
	}
}

// RepromptOnboarding clears FIVE attributes and it has to clear all five in one
// write. After the fold the observer only advances an admission with no
// user_sub, so a reset that left one behind would re-greet the chat forever and
// never admit it (BUGS O1).
func TestRepromptOnboarding_ClearsEveryVerdictAttribute(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	setAdmission(t, s, "telegram:1", map[string]string{
		attrOnbStatus:     statusApproved,
		attrOnbPromptedAt: onbodTS(time.Now()),
		attrOnbUserSub:    "github:alice",
		attrOnbGate:       "github:org=co",
		attrOnbQueuedAt:   onbodTS(time.Now()),
		attrOnbAdmittedAt: onbodTS(time.Now()),
	})

	if err := s.RepromptOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	it := admissionItem(t, s, "telegram:1")
	if got := it.Attrs[attrOnbStatus]; got != statusAwaitingMessage {
		t.Errorf("status = %q, want %q", got, statusAwaitingMessage)
	}
	for _, attr := range []string{
		attrOnbPromptedAt, attrOnbUserSub, attrOnbGate, attrOnbQueuedAt, attrOnbAdmittedAt,
	} {
		if got, ok := it.Attrs[attr]; ok {
			t.Errorf("%s = %q after a reprompt; want it removed — one verdict attribute "+
				"left behind re-greets the chat and never admits it (BUGS O1)", attr, got)
		}
	}
	if it.Attrs[attrOnbCreated] == "" {
		t.Error("created was cleared; the reset must not move when the chat first appeared")
	}
}

// The instants are DECLARED 20 bytes wide, so every driver refuses a value of
// another width rather than letting it sort into the wrong place (BUGS L97).
// The re-greet, the queue position and the daily quota are all byte comparisons
// on these attributes.
func TestOrderedInstantsRefuseAnotherWidth(t *testing.T) {
	s := openEntities(t)

	if err := s.InsertOnboarding("telegram:1", "telegram:1"); err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{
		attrOnbCreated, attrOnbPromptedAt, attrOnbQueuedAt, attrOnbAdmittedAt,
	} {
		_, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
			Op: objstore.Update, Entity: Onboarding,
			Key:   objstore.Key{Partition: OwnerOnbod, Sort: "telegram:1"},
			Attrs: map[string]string{attr: "2026-01-01"},
		}})
		if err == nil {
			t.Errorf("%s accepted a 10-byte value; a comparison on it is a byte "+
				"comparison, so a short value sorts into the wrong place", attr)
			continue
		}
		if !strings.Contains(err.Error(), "declared ordered width is 20") {
			t.Errorf("%s refused with %v, want the ordered-width reason", attr, err)
		}
	}
}

// onbodTSWidth is the width the declarations use, and it has to be the width
// the layout produces — a constant that drifted from the format would refuse
// every write instead of guarding one.
func TestOnbodTSWidthMatchesTheLayout(t *testing.T) {
	if got := len(onbodTS(time.Now())); got != onbodTSWidth {
		t.Errorf("onbodTS renders %d bytes, declared %d", got, onbodTSWidth)
	}
}

// ListOnboarding orders by an ATTRIBUTE, so the sort runs in this process over
// a whole-collection read. Two admissions created in the same second come back
// in the coordinate's order, which the ORDER BY it replaces did not promise.
func TestListOnboardingOrdersByCreatedDescending(t *testing.T) {
	s := openEntities(t)

	for _, jid := range []string{"telegram:a", "telegram:b", "telegram:c"} {
		if err := s.InsertOnboarding(jid, jid); err != nil {
			t.Fatal(err)
		}
	}
	setAdmission(t, s, "telegram:a", map[string]string{attrOnbCreated: "2026-03-01T00:00:00Z"})
	setAdmission(t, s, "telegram:b", map[string]string{attrOnbCreated: "2026-01-01T00:00:00Z"})
	setAdmission(t, s, "telegram:c", map[string]string{attrOnbCreated: "2026-02-01T00:00:00Z"})

	rows, err := s.ListOnboarding("")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.JID)
	}
	want := []string{"telegram:a", "telegram:c", "telegram:b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (created DESC)", got, want)
	}
}

// The gate's `enabled` flag is stored as the int the resreg engine also renders
// (resreg/entity.go itemFor), so the two readers of these items agree byte for
// byte. A gate that read back disabled would silently stop admitting.
func TestGateEnablementRoundTrips(t *testing.T) {
	s := openEntities(t)

	if err := s.PutGate("github:org=co", 10); err != nil {
		t.Fatal(err)
	}
	gates, err := s.EnabledGates()
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 1 || !gates[0].Enabled || gates[0].LimitPerDay != 10 {
		t.Fatalf("enabled gates = %+v, want one enabled gate at 10/day", gates)
	}
	// A second Put moves the LIMIT and must leave enablement alone, which is
	// what carrying `enabled` as the merge's Init half buys.
	if err := s.EnableGate("github:org=co", false); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGate("github:org=co", 25); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListGates()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Enabled || all[0].LimitPerDay != 25 {
		t.Errorf("gates = %+v, want one DISABLED gate at 25/day", all)
	}
	enabled, err := s.EnabledGates()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Errorf("enabled gates = %+v, want none", enabled)
	}
}

package conformance

// writes.go holds the bodies about what a GuardedBatch changes and what it says
// when it changes nothing. Each is derived from a statement a daemon runs today
// that the two primitives could not express before.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// onboarding is the entity the removal body writes: store.RepromptOnboarding's
// row, whose five-attribute reset is the reason Update has a REMOVE half.
const onboarding = "onboarding"

// invites is the entity the counter bodies write: store/invites.go's row, whose
// used_count is the increment the interface deliberately does not carry.
const invites = "invites"

// redemptions is the entity the companion write lands in: the row a redemption
// records beside the counter it moves.
const redemptions = "redemptions"

// counted is the entity the ordered-width body writes.
const counted = "ordered_log"

// tsWidth is core.TS's width, which is what an entity ordered by time declares.
const tsWidth = 30

func init() {
	// One declaration, read by every driver: the width lives with the entity,
	// not with a constructor argument some driver could be handed differently.
	objstore.OrderedSort(counted, tsWidth)
	objstore.OrderedAttr(counted, "at", tsWidth)
}

// RemovalTakesTheAttributeAwayRatherThanEmptyingIt is
// store.RepromptOnboarding, whose comment says what a partial reset costs:
// after the fold the observer only advances a row with `user_sub IS NULL`, so a
// reset that left one of the five attributes behind would re-greet the chat and
// then never admit it (BUGS O1).
//
// Absent is the only null, so "" is not a reset: an attribute written empty is
// one Present still finds. Taking it away is the only operation that makes it
// missing, and DynamoDB has it as REMOVE.
//
// The five go in ONE write with the status change, because that is what makes
// the reset atomic. Five writes could interleave with the observer's next tick.
func RemovalTakesTheAttributeAwayRatherThanEmptyingIt(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, onboarding)
	key := objstore.Key{Partition: tenant, Sort: "web:chat/42"}
	put(t, s, onboarding, key, map[string]string{
		"status": "admitted", "prompted_at": "T1", "user_sub": "google:u1",
		"gate": "open", "queued_at": "T2", "admitted_at": "T3", "paired": "yes",
	})

	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding, Key: key,
		Attrs:  map[string]string{"status": "awaiting_message"},
		Remove: []string{"prompted_at", "user_sub", "gate", "queued_at", "admitted_at"}})
	got := one(t, s, onboarding, key)
	for _, gone := range []string{"prompted_at", "user_sub", "gate", "queued_at", "admitted_at"} {
		if _, still := got.Attrs[gone]; still {
			t.Errorf("the reset left %s behind: %v", gone, got.Attrs)
		}
	}
	if got.Attrs["status"] != "awaiting_message" {
		t.Errorf("the reset did not set status: %v", got.Attrs)
	}
	if got.Attrs["paired"] != "yes" {
		t.Errorf("the reset took the pairing edge, which is the user's consent and not "+
			"admission state: %v", got.Attrs)
	}
	// A removed attribute is ABSENT, which is what the observer's predicate
	// tests. An attribute set to "" would still be Present, and the row would
	// never advance again.
	if items := s2keys(t, s, objstore.Query{Entity: onboarding, Partition: tenant,
		Where: []objstore.Cmp{{Attr: "user_sub", Op: objstore.Absent}}}); !sameOrder(items, []string{key.Sort}) {
		t.Errorf("the reset row does not read as user_sub-absent: %v", items)
	}

	// Removing a name the item does not carry is a no-op, as DynamoDB's REMOVE
	// is — otherwise every idempotent reset would need a read first.
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding, Key: key,
		Remove: []string{"gate", "never_written"}})

	// A removal is still an Update, so it obeys the condition it carries and it
	// requires the item to exist.
	missing := objstore.Key{Partition: tenant, Sort: "web:chat/none"}
	if commit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding,
		Key: missing, Remove: []string{"gate"}}) {
		t.Error("a removal on a missing item committed")
	}
	if commit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding, Key: key,
		Remove: []string{"paired"},
		Cond:   objstore.Cond{Where: []objstore.Cmp{{Attr: "status", Op: objstore.Eq, Value: "admitted"}}},
	}) {
		t.Error("a conditional removal committed against a condition that does not hold")
	}
	if got = one(t, s, onboarding, key); got.Attrs["paired"] != "yes" {
		t.Errorf("the refused removal took the attribute anyway: %v", got.Attrs)
	}

	// Setting and removing one attribute in one write has no obviously right
	// answer, so neither engine guesses and the batch is refused as the mistake
	// it is.
	if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: onboarding, Key: key,
		Attrs: map[string]string{"gate": "open"}, Remove: []string{"gate"}}}); err == nil {
		t.Error("a write that both sets and removes one attribute was accepted")
	}
	// Only an Update has a REMOVE half: a Put already replaces the whole
	// document and a Delete takes the item.
	if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: onboarding, Key: key,
		Attrs: map[string]string{"status": "x"}, Remove: []string{"paired"}}}); err == nil {
		t.Error("a Put carrying a REMOVE half was accepted")
	}
}

// slipOnce is a driver that lets one writer in between a read and the write
// that follows it, firing at most once. It is the only test double in this
// suite, and it is here because a compare-and-swap can only be proved by an
// interleave a race makes likely and this makes certain.
type slipOnce struct {
	objstore.Store
	at   func()
	done bool
}

func (s *slipOnce) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := s.Store.Query(ctx, q)
	if err == nil && !s.done {
		s.done = true
		s.at()
	}
	return items, err
}

// CountersAreACompareAndSwapProtocol is the decision about the atomic
// increment, run as a test rather than written down as an intention.
//
// store/invites.go does `used_count = used_count + 1 WHERE used_count <
// max_uses … RETURNING`, and routd's RecordDeliveryFailure does the same shape
// with the comment "One statement does both, so a crash between them is
// impossible". Neither goes on the interface: every attribute value is a string,
// and DynamoDB's in-place increment needs a Number — "You can also use SET to
// add or subtract from an attribute that is of type Number", and ADD "supports
// only number and set data types". An increment on the interface would be an
// operation only the SQL driver could honour.
//
// So it is objstore.Increment, a protocol over the two primitives: read the
// value, write with the read value as the condition, retry the lost race. This
// body is the proof that the protocol gives what the one statement gave — no
// lost update — because a CAS whose condition were dropped would pass every
// single-threaded assertion above.
//
// The first half FORCES the interleave rather than racing for it. Measured
// 2026-09-07: dropping the compare-and-swap and running only the concurrent
// half below turned this body red 3 times in 12, because SQLite serialises
// writers and the collision has to fall in the window between a read and its
// write. A mutation that changes the answer for a third of runs reads as
// absence, so the interleave is made certain instead of likely.
func CountersAreACompareAndSwapProtocol(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, invites)
	key := objstore.Key{Partition: tenant, Sort: "inv/party"}
	put(t, s, invites, key, map[string]string{"used_count": "0", "max_uses": "5"})

	// One writer moves the counter between the protocol's read and the write
	// that follows it, which is exactly the interleave the condition exists to
	// lose. The guard then does not hold, the protocol reads again, and the
	// increment lands on the value it actually found: 7 + 1. A protocol that
	// wrote without the condition clobbers the 7 with 0 + 1, and the other
	// writer's update is gone.
	slipped := &slipOnce{Store: s, at: func() {
		mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: invites, Key: key,
			Attrs: map[string]string{"used_count": "7"}})
	}}
	n, committed, err := objstore.Increment(ctx, slipped, invites, key, "used_count", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !committed || n != 8 {
		t.Fatalf("an increment over a counter that moved under it reported committed=%v n=%d, "+
			"want true and 8; a write that landed on 1 overwrote the other writer's 7, which "+
			"is the lost update the condition on the read value exists to stop", committed, n)
	}
	if got := one(t, s, invites, key); got.Attrs["used_count"] != "8" {
		t.Fatalf("used_count is %q, want 8", got.Attrs["used_count"])
	}
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: invites, Key: key,
		Attrs: map[string]string{"used_count": "0"}})

	const callers = 8
	var (
		mu      sync.Mutex
		seen    = map[int]int{}
		refused int
		errs    []error
		wg      sync.WaitGroup
	)
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			// The caller's own guard rides on the same write, so the redemption
			// bound is decided by the engine against the row this write changes
			// and not by a read that has since gone stale.
			n, committed, err := objstore.Increment(ctx, s, invites, key, "used_count", 1,
				[]objstore.Cmp{{Attr: "used_count", Op: objstore.Lt, Value: "5"}}, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, err)
			case !committed:
				refused++
			default:
				seen[n]++
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("an increment reported an error: %v", errs[0])
	}
	if refused != callers-5 {
		t.Errorf("%d redemptions were refused, want %d — the bound is 5 of %d callers",
			refused, callers-5, callers)
	}
	for want := 1; want <= 5; want++ {
		if seen[want] != 1 {
			t.Errorf("%d callers were told the counter reached %d, want exactly 1 — two "+
				"callers with the same post-image is a LOST update, which is what the "+
				"condition on the read value exists to stop: %v", seen[want], want, seen)
		}
	}
	if got := one(t, s, invites, key); got.Attrs["used_count"] != "5" {
		t.Errorf("used_count is %q after %d contended redemptions, want 5",
			got.Attrs["used_count"], callers)
	}
}

// CountersCarryTheirCompanionWrites is the other half of a redemption, and the
// reason Increment takes companion actions at all.
//
// store/invites.go:265 increments `used_count` and writes the redemption, ACL
// and audit state in the SAME transaction. A counter helper that committed only
// the counter would burn an invite with no grant — the failure
// store.RestoreInvite exists to compensate for across two databases, by
// decrementing after the fact. Inside one store the compensation is
// unnecessary: the companions ride in the protocol's own batch.
//
// Both halves of the assertion matter. A win writes the companion, and a
// REFUSAL writes nothing — not the counter, not the companion — because the
// batch is the unit.
func CountersCarryTheirCompanionWrites(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, invites, redemptions)
	key := objstore.Key{Partition: tenant, Sort: "inv/party"}
	put(t, s, invites, key, map[string]string{"used_count": "0", "max_uses": "1"})

	record := func(who string) objstore.Write {
		return objstore.Write{
			Op: objstore.Put, Entity: redemptions,
			Key:   objstore.Key{Partition: tenant, Sort: "red/" + who},
			Attrs: map[string]string{"user_sub": who},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		}
	}
	bound := []objstore.Cmp{{Attr: "used_count", Op: objstore.Lt, Value: "1"}}

	n, committed, err := objstore.Increment(ctx, s, invites, key, "used_count", 1,
		bound, []objstore.Write{record("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if !committed || n != 1 {
		t.Fatalf("the first redemption reported committed=%v count=%d, want true and 1", committed, n)
	}
	if got := s2keys(t, s, objstore.Query{Entity: redemptions, Partition: tenant}); !sameOrder(
		got, []string{"red/alice"}) {
		t.Errorf("the redemption record is %v, want red/alice — a counter that commits without "+
			"its companion burns the invite and grants nothing", got)
	}

	// The bound is spent, so the next redemption is refused — and its companion
	// must not land on its own, which is exactly the state RestoreInvite was
	// written to clean up after.
	_, committed, err = objstore.Increment(ctx, s, invites, key, "used_count", 1,
		bound, []objstore.Write{record("bob")})
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Error("a redemption past max_uses committed")
	}
	if got := s2keys(t, s, objstore.Query{Entity: redemptions, Partition: tenant}); !sameOrder(
		got, []string{"red/alice"}) {
		t.Errorf("the refused redemption left %v; a refused batch writes nothing", got)
	}
	if got := one(t, s, invites, key); got.Attrs["used_count"] != "1" {
		t.Errorf("used_count is %q after a refused redemption, want 1", got.Attrs["used_count"])
	}

	// A companion naming the counter's own item is two actions on one item,
	// which is the bound every driver carries.
	if _, _, err := objstore.Increment(ctx, s, invites, key, "used_count", 1, nil,
		[]objstore.Write{{Op: objstore.Update, Entity: invites, Key: key,
			Attrs: map[string]string{"max_uses": "9"}}}); err == nil {
		t.Error("a companion write on the counter's own item was accepted")
	}
}

// OrderedWidthsAreRefusedRatherThanMisSorted enforces the one modelling rule,
// which live data already breaks: messages.timestamp on krons carries seven
// different widths across 24,013 rows, and 32 pairs share a second across two of
// them. A comparison on that column is a BYTE comparison, so at those pairs byte
// order and time order disagree — BUGS L97, read back off the rows rather than
// off the code.
//
// The rule cannot be discipline, because the failure is silent: a short value
// sorts into the wrong place instead of failing. So the writer declares the
// width once and every driver refuses another, the way checkIdent refuses a name
// it would write into a statement.
func OrderedWidthsAreRefusedRatherThanMisSorted(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, counted)
	good := core.TS(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	if len(good) != tsWidth {
		t.Fatalf("core.TS renders %d bytes, and this body declares %d", len(good), tsWidth)
	}
	put(t, s, counted, objstore.Key{Partition: tenant, Sort: good},
		map[string]string{"at": good, "seq": "0"})

	// The width the seven-width column actually carried: RFC3339 to the second,
	// which is shorter and sorts before a fraction of the same second.
	short := good[:19] + "Z"
	for _, c := range []struct {
		name  string
		write objstore.Write
	}{
		{"a short sort key", objstore.Write{Op: objstore.Put, Entity: counted,
			Key: objstore.Key{Partition: tenant, Sort: short}, Attrs: map[string]string{"at": good}}},
		{"a short ordered attribute", objstore.Write{Op: objstore.Put, Entity: counted,
			Key: objstore.Key{Partition: tenant, Sort: good}, Attrs: map[string]string{"at": short}}},
		{"a short value in a write's condition", objstore.Write{Op: objstore.Update, Entity: counted,
			Key: objstore.Key{Partition: tenant, Sort: good}, Attrs: map[string]string{"seq": "1"},
			Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: "at", Op: objstore.Gt, Value: short}}}}},
	} {
		if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{c.write}); err == nil {
			t.Errorf("%s was accepted; it sorts into the wrong place instead of failing", c.name)
		}
	}
	if items := s2keys(t, s, objstore.Query{Entity: counted, Partition: tenant}); len(items) != 1 {
		t.Errorf("the refused writes landed anyway: %v", items)
	}

	// A read compares the same bytes, so the same rule holds on the way in.
	for _, c := range []struct {
		name string
		q    objstore.Query
	}{
		{"a short range bound", objstore.Query{Entity: counted, Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.GtKey, Value: short}}},
		{"a short exact key", objstore.Query{Entity: counted, Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: short}}},
		{"a short filter value", objstore.Query{Entity: counted, Partition: tenant,
			Where: []objstore.Cmp{{Attr: "at", Op: objstore.Ge, Value: short}}}},
	} {
		if _, err := s.Query(ctx, c.q); err == nil {
			t.Errorf("%s was accepted; a comparison at the wrong width answers by byte order "+
				"instead of failing, so a watermark read skips rows permanently", c.name)
		}
	}

	// A subtree read is the one sort-key operator whose operand is NOT a key: a
	// prefix is a leading fragment, so it is shorter by definition and the width
	// rule cannot apply to it.
	if _, err := s.Query(ctx, objstore.Query{Entity: counted, Partition: tenant,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: good[:10]}}); err != nil {
		t.Errorf("a prefix read of an ordered entity was refused: %v", err)
	}
	// An undeclared attribute is unconstrained: the rule is a declaration a
	// writer makes, not a guess the driver makes about every value it sees.
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: counted,
		Key:   objstore.Key{Partition: tenant, Sort: good},
		Attrs: map[string]string{"seq": "22"}})
	// And an entity nobody declared is unconstrained everywhere.
	other := newStore(t, tokens)
	put(t, other, tokens, objstore.Key{Partition: tenant, Sort: short},
		map[string]string{"at": short})
}

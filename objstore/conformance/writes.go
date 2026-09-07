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

// invites is the entity the counter body writes: store/invites.go's row, whose
// used_count is the increment the interface deliberately does not carry.
const invites = "invites"

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
	if out := commit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding,
		Key: missing, Remove: []string{"gate"}}); out.Committed {
		t.Error("a removal on a missing item committed")
	}
	if out := commit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: onboarding, Key: key,
		Remove: []string{"paired"},
		Cond:   objstore.Cond{Where: []objstore.Cmp{{Attr: "status", Op: objstore.Eq, Value: "admitted"}}},
	}); out.Committed {
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

// ConditionalReplaceNeedsTheItemItReplaces is the Put a caller reaches for when
// the new document is not a function of the old one — a whole-item replace under
// a condition on the item being replaced.
//
// DynamoDB's transactional Put takes "an optional condition expression that must
// be satisfied for the write to succeed", and it fails the same way against an
// item that is not there: an absent item carries no attribute a condition could
// hold on. So the condition implies presence, and a driver that let the insert
// branch run would commit exactly the write the condition existed to refuse.
func ConditionalReplaceNeedsTheItemItReplaces(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	key := objstore.Key{Partition: tenant, Sort: "tok/live"}
	put(t, s, tokens, key, map[string]string{"family": "f1", "sub": "alice", "gen": "1"})

	if out := commit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: key,
		Attrs: map[string]string{"family": "f2", "gen": "2"},
		Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: "gen", Op: objstore.Eq, Value: "9"}}},
	}); out.Committed {
		t.Error("a conditional replace committed against a generation that does not hold")
	}
	if got := one(t, s, tokens, key); got.Attrs["gen"] != "1" || got.Attrs["sub"] != "alice" {
		t.Errorf("the refused replace changed the item: %v", got.Attrs)
	}

	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: key,
		Attrs: map[string]string{"family": "f2", "gen": "2"},
		Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: "gen", Op: objstore.Eq, Value: "1"}}}})
	got := one(t, s, tokens, key)
	if len(got.Attrs) != 2 || got.Attrs["gen"] != "2" {
		t.Errorf("the conditional replace did not replace the whole document: %v", got.Attrs)
	}

	// The condition implies presence, whatever it says. An item that is not
	// there must not be CREATED by a write whose condition was about an item.
	missing := objstore.Key{Partition: tenant, Sort: "tok/absent"}
	if out := commit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: missing,
		Attrs: map[string]string{"family": "f3"},
		Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: "gen", Op: objstore.Absent}}},
	}); out.Committed {
		t.Error("a conditional replace created an item that was not there")
	}
	if items := point(t, s, tokens, missing); len(items) != 0 {
		t.Errorf("the refused conditional replace inserted %v", items)
	}
	if out := commit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: missing,
		Attrs: map[string]string{"family": "f3"},
		Cond:  objstore.Cond{Item: objstore.ItemPresent}}); out.Committed {
		t.Error("a replace asking for presence created the item")
	}
	// Absent AND carrying attributes can never hold, so it is a caller mistake
	// rather than a condition that always refuses.
	if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: tokens, Key: missing, Attrs: map[string]string{"family": "f3"},
		Cond: objstore.Cond{Item: objstore.ItemAbsent,
			Where: []objstore.Cmp{{Attr: "gen", Op: objstore.Eq, Value: "1"}}}}}); err == nil {
		t.Error("a Put asking the item to be absent AND to carry an attribute was accepted")
	}
}

// RefusalNamesWhatDidNotHold is the answer a bare false could not give.
//
// Any failed check and any zero-row write both refuse, and callers read that one
// bit as a semantic verdict. It cannot carry one: "someone else won the race"
// and "the row was already in that state" are the same bit and different
// incidents — the first is a contended claim working, the second is reuse of a
// spent credential, which kills a family.
//
// So the refusal names the action and carries the item as the engine held it,
// which is DynamoDB's ReturnValuesOnConditionCheckFailure plus the
// CancellationReasons index.
func RefusalNamesWhatDidNotHold(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	live := objstore.Key{Partition: tenant, Sort: "tok/live"}
	spent := objstore.Key{Partition: tenant, Sort: "tok/spent"}
	gate := objstore.Key{Partition: tenant, Sort: "tok/gate"}
	put(t, s, tokens, live, map[string]string{"family": "f1"})
	put(t, s, tokens, spent, map[string]string{"family": "f1", "used_at": "T0"})
	put(t, s, tokens, gate, map[string]string{"family": "f1", "state": "open"})

	// A write whose own condition did not hold: the index is the write's, the
	// kind is WriteRefused, and the item is the pre-image the condition saw.
	out := commit(t, s, ctx, claim(spent))
	if out.Committed || out.Refused == nil {
		t.Fatalf("claiming a spent token gave %+v, want an attributed refusal", out)
	}
	if out.Refused.Kind != objstore.WriteRefused || out.Refused.Index != 0 {
		t.Errorf("the refusal is %v at %d, want a WriteRefused at 0",
			out.Refused.Kind, out.Refused.Index)
	}
	if out.Refused.Entity != tokens || out.Refused.Key != spent {
		t.Errorf("the refusal names %s/%v, want %s/%v",
			out.Refused.Entity, out.Refused.Key, tokens, spent)
	}
	if out.Refused.Item == nil || out.Refused.Item.Attrs["used_at"] != "T0" {
		t.Fatalf("the refusal carries %v, want the pre-image with its used_at — without it "+
			"a lost race and a reused credential are the same answer", out.Refused.Item)
	}

	// The same refusal against an item that is NOT there carries no pre-image,
	// which is a third answer again: nobody won a race, the coordinate is empty.
	out = commit(t, s, ctx, claim(objstore.Key{Partition: tenant, Sort: "tok/never"}))
	if out.Committed || out.Refused == nil {
		t.Fatalf("claiming a token that was never issued gave %+v", out)
	}
	if out.Refused.Item != nil {
		t.Errorf("a refusal on a missing item carried the item %v", out.Refused.Item)
	}

	// A CHECK that did not hold names the check's index, in the check list, and
	// the item the batch does not modify. Without the kind, index 0 of the
	// checks and index 0 of the writes are the same number.
	res, err := s.GuardedBatch(ctx,
		[]objstore.Check{{Entity: tokens, Key: gate,
			Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: "state", Op: objstore.Eq, Value: "closed"}}}}},
		[]objstore.Write{claim(live)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed || res.Refused == nil {
		t.Fatalf("a batch whose check does not hold gave %+v", res)
	}
	if res.Refused.Kind != objstore.CheckRefused || res.Refused.Key != gate {
		t.Errorf("the refusal is %v on %v, want a CheckRefused on the gate",
			res.Refused.Kind, res.Refused.Key)
	}
	if res.Refused.Item == nil || res.Refused.Item.Attrs["state"] != "open" {
		t.Errorf("the check refusal carries %v, want the gate as the engine held it",
			res.Refused.Item)
	}
	if got := one(t, s, tokens, live); got.Attrs["used_at"] != "" {
		t.Error("the refused batch still applied its write")
	}

	// The index is the position in its own list, not in the batch, so a caller
	// with several writes learns WHICH one refused.
	res, err = s.GuardedBatch(ctx, nil, []objstore.Write{claim(live), claim(spent)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed || res.Refused == nil || res.Refused.Index != 1 {
		t.Fatalf("the second write refused the batch and the refusal reports %+v", res.Refused)
	}
	// A committed batch refuses nothing, so the two answers cannot be confused.
	if res = commit(t, s, ctx, claim(live)); !res.Committed || res.Refused != nil {
		t.Errorf("a committed claim reported %+v", res)
	}
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
// lost update under contention — because a CAS whose condition were dropped
// would pass every single-threaded assertion above.
func CountersAreACompareAndSwapProtocol(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, invites)
	key := objstore.Key{Partition: tenant, Sort: "inv/party"}
	put(t, s, invites, key, map[string]string{"used_count": "0", "max_uses": "5"})

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
			n, out, err := objstore.Increment(ctx, s, invites, key, "used_count", 1,
				[]objstore.Cmp{{Attr: "used_count", Op: objstore.Lt, Value: "5"}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, err)
			case !out.Committed:
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
		{"a short BETWEEN bound", objstore.Query{Entity: counted, Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.BetweenKey, Value: short, Upper: good}}},
		{"a short cursor", objstore.Query{Entity: counted, Partition: tenant, After: short}},
		{"a short filter value", objstore.Query{Entity: counted, Partition: tenant,
			Where: []objstore.Cmp{{Attr: "at", Op: objstore.Ge, Value: short}}}},
	} {
		if _, err := s.Query(ctx, c.q); err == nil {
			t.Errorf("%s was accepted; a cursor that compares the wrong width skips rows "+
				"permanently, because a cursor only moves forward", c.name)
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

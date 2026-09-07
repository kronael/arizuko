// Package conformance is the suite every objstore driver passes. The test
// bodies are exported and live here rather than beside one driver, so a second
// driver runs the IDENTICAL bodies and cannot quietly answer a question
// differently (spec 6/37 step 4).
//
// That substitution is what a vendor API would have imposed for free and what
// arizuko's own two-primitive surface has to earn: an operation that cannot be
// expressed for every driver does not belong on the interface, and the only way
// to know is to run the same assertions against each one.
//
// The five bodies below are the behaviours authd depends on today, each derived
// from a statement in authd/authstore rather than from what an interface might
// plausibly need.
package conformance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// Factory hands one test an EMPTY store carrying the entities it names. Two
// calls in one test return two stores; a driver that cannot give a test its own
// empty store cannot be conformance-tested.
type Factory func(t *testing.T, entities ...string) objstore.Store

// Run runs every conformance body against the driver newStore builds.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	for _, c := range []struct {
		name string
		body func(*testing.T, Factory)
	}{
		{"OneClaimWinsAndTheLosersAreNotErrors", OneClaimWinsAndTheLosersAreNotErrors},
		{"ClaimAndInsertHappenTogetherOrNotAtAll", ClaimAndInsertHappenTogetherOrNotAtAll},
		{"PrefixDoesNotWiden", PrefixDoesNotWiden},
		{"PointReadReportsTombstonesRatherThanHidingThem", PointReadReportsTombstonesRatherThanHidingThem},
		{"SortKeyOrderIsTimeOrder", SortKeyOrderIsTimeOrder},
		{"ProjectionLeavesUnprojectedAttributesInTheEngine", ProjectionLeavesUnprojectedAttributesInTheEngine},
		{"WriteSemanticsAreTheSameOnEveryDriver", WriteSemanticsAreTheSameOnEveryDriver},
		{"BatchRefusesWhatNoDriverMayAccept", BatchRefusesWhatNoDriverMayAccept},
	} {
		t.Run(c.name, func(t *testing.T) { c.body(t, newStore) })
	}
}

// tokens is the entity every claim body writes: authd's refresh_tokens, whose
// row carries the two tombstones that make a rotation one-time and a family
// killable.
const tokens = "refresh_tokens"

// tenant is the partition every body writes under. A subtree has to sit inside
// ONE partition for a prefix to cover it (spec 6/40 §1).
const tenant = "acme"

// OneClaimWinsAndTheLosersAreNotErrors is authd's ClaimRefreshToken: N callers
// present the same refresh token at once and exactly one may rotate it. A
// second winner forks the family, and reuse detection then never fires.
//
// The half this body exists for is the LOSER's answer. "Someone else claimed
// it" is the normal outcome of a contended claim, not a fault — authd reports
// it as won=false with a nil error — so a driver that raised it as an error
// would make every caller unwrap a sentinel to find out whether its own logic
// worked. Run this with -race: the assertion is about concurrent callers.
func OneClaimWinsAndTheLosersAreNotErrors(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	key := objstore.Key{Partition: tenant, Sort: "tok/live"}
	put(t, s, tokens, key, map[string]string{"family": "f1", "sub": "alice"})

	const callers = 8
	var (
		mu       sync.Mutex
		won      int
		refused  int
		firstErr error
		wg       sync.WaitGroup
	)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			ok, err := s.GuardedBatch(ctx, nil, []objstore.Write{claim(key)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				if firstErr == nil {
					firstErr = err
				}
			case ok:
				won++
			default:
				refused++
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("a losing claim reported an error: %v — losing a race is a normal outcome, "+
			"and a caller must be able to tell it from a fault", firstErr)
	}
	if won != 1 {
		t.Errorf("%d of %d concurrent claims won, want exactly 1", won, callers)
	}
	if refused != callers-1 {
		t.Errorf("%d claims were refused, want %d", refused, callers-1)
	}
	got := one(t, s, tokens, key)
	if _, ok := got.Attrs["used_at"]; !ok {
		t.Errorf("the winning claim left no used_at on the item: %v", got.Attrs)
	}
}

// ClaimAndInsertHappenTogetherOrNotAtAll is authd's ClaimAndRotateRefresh: the
// presented token is claimed and its successor inserted in ONE batch, or
// neither happens.
//
// Splitting the two let a competing redeem's family revoke commit in the gap,
// and a family killed for reuse walked away with a live 30-day credential
// (BUGS F36). Each caller here inserts its OWN successor, and every one of
// those inserts would succeed on its own — so a successor from a losing caller
// is atomicity failing, not a condition passing.
func ClaimAndInsertHappenTogetherOrNotAtAll(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	presented := objstore.Key{Partition: tenant, Sort: "tok/live"}
	put(t, s, tokens, presented, map[string]string{"family": "f1", "sub": "alice"})

	const callers = 6
	var (
		mu     sync.Mutex
		winner string
		won    int
		errs   []error
		wg     sync.WaitGroup
	)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		successor := objstore.Key{Partition: tenant, Sort: "tok/next-" + string(rune('a'+i))}
		go func() {
			defer wg.Done()
			ok, err := s.GuardedBatch(ctx, nil, []objstore.Write{
				claim(presented),
				{Op: objstore.Put, Entity: tokens, Key: successor,
					Attrs: map[string]string{"family": "f1", "sub": "alice"},
					Cond:  objstore.Cond{Item: objstore.ItemAbsent}},
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, err)
			case ok:
				won++
				winner = successor.Sort
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("a rotation reported an error: %v", errs[0])
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent rotations won, want exactly 1", won, callers)
	}
	successors := s2keys(t, s, objstore.Query{
		Entity: tokens, Partition: tenant,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: "tok/next", Sep: "-"},
	})
	if len(successors) != 1 || successors[0] != winner {
		t.Errorf("successors = %v, want only the winner's %q — a loser's insert landed "+
			"without its claim, so the batch was not all-or-nothing", successors, winner)
	}

	// The deterministic half: the token is spent now, so the same batch must
	// leave NOTHING behind, not even the half that would succeed on its own.
	orphan := objstore.Key{Partition: tenant, Sort: "tok/next-z"}
	ok, err := s.GuardedBatch(ctx, nil, []objstore.Write{
		claim(presented),
		{Op: objstore.Put, Entity: tokens, Key: orphan,
			Attrs: map[string]string{"family": "f1"},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent}},
	})
	if err != nil {
		t.Fatalf("re-claiming a spent token: %v", err)
	}
	if ok {
		t.Fatal("a spent token was claimed a second time")
	}
	if items := point(t, s, tokens, orphan); len(items) != 0 {
		t.Errorf("the refused batch still inserted %q", orphan.Sort)
	}
}

// PrefixDoesNotWiden is containment, the predicate every scoped read asks.
//
// A subtree is a string prefix on the sort key and nothing else, and the
// comparison has to be exact. SQL LIKE is the wrong predicate twice over: `_`
// and `%` are wildcards on both engines, and LIKE is case-INSENSITIVE for ASCII
// on SQLite. Measured: deleting folder `a_b` also deleted `aXb`'s rows, and
// deleting `coach` also deleted `Coach`'s, on an instance that HAS carried a
// Coach/coach pair (BUGS L66, L122, L252).
//
// Every sibling below is a folder LIKE would have swept in. A driver that
// widens fails here whatever it widens with.
func PrefixDoesNotWiden(t *testing.T, newStore Factory) {
	s := newStore(t, "routes")
	for _, sort := range []string{
		"a_b", "a_b/one", "a_b/deep/two", // the subtree itself
		"aXb", "aXb/x", // `_` read as a wildcard
		"a%b", "a%b/one", "aZZb", "aZZb/z", // `%` read as a wildcard
		"A_B", "A_B/one", // a case-differing sibling
		"a_bb", "a_b2", // a prefix that is not a subtree boundary
	} {
		put(t, s, "routes", objstore.Key{Partition: tenant, Sort: sort}, map[string]string{"seq": "1"})
	}
	for _, c := range []struct {
		folder string
		want   []string
	}{
		{"a_b", []string{"a_b", "a_b/deep/two", "a_b/one"}},
		{"a%b", []string{"a%b", "a%b/one"}},
		{"A_B", []string{"A_B", "A_B/one"}},
	} {
		got := s2keys(t, s, objstore.Query{
			Entity: "routes", Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: c.folder, Sep: "/"},
		})
		if !sameKeys(got, c.want) {
			t.Errorf("subtree %q returned %v, want %v", c.folder, got, c.want)
		}
	}
}

// PointReadReportsTombstonesRatherThanHidingThem is authd's
// RefreshTokenBySecret, which applies no liveness predicate at all.
//
// Refusing to FIND a spent token makes reuse indistinguishable from a token
// that never existed, and those are different answers: the first kills a
// family, the second is a stranger presenting a guess. So a point read reports
// the tombstones and the caller decides. A driver that filtered them for the
// caller would delete an incident's evidence.
func PointReadReportsTombstonesRatherThanHidingThem(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	spent := objstore.Key{Partition: tenant, Sort: "tok/spent"}
	revoked := objstore.Key{Partition: tenant, Sort: "tok/revoked"}
	put(t, s, tokens, spent, map[string]string{"family": "f1"})
	put(t, s, tokens, revoked, map[string]string{"family": "f2"})
	mustCommit(t, s, ctx, claim(spent))
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: tokens, Key: revoked,
		Attrs: map[string]string{"revoked_at": core.NowTS()}})

	got := one(t, s, tokens, spent)
	if _, ok := got.Attrs["used_at"]; !ok {
		t.Errorf("the spent token came back without its used_at: %v — reuse is then "+
			"indistinguishable from a token that never existed", got.Attrs)
	}
	if got = one(t, s, tokens, revoked); got.Attrs["revoked_at"] == "" {
		t.Errorf("the revoked token came back without its revoked_at: %v", got.Attrs)
	}
	never := point(t, s, tokens, objstore.Key{Partition: tenant, Sort: "tok/never-issued"})
	if len(never) != 0 {
		t.Errorf("a token that was never issued read back as %v", never)
	}
}

// SortKeyOrderIsTimeOrder pins the layout rule, not the driver's ORDER BY.
//
// A document store has no AUTOINCREMENT to order by, so history is ordered by a
// time key the WRITER produces, fixed width so string order is time order
// (spec 6/37 rule 4). core.TS is that layout. time.RFC3339Nano is not: it
// strips trailing zeros, so a whole second renders `…:41Z` and a fraction of
// the SAME second renders `…:41.882458214Z`, and `.` (0x2E) sorts before `Z`
// (0x5A) — the later instant sorts FIRST. Every chat /approve wrote its
// re-issue trigger into that blind spot and no held call was ever re-issued
// (BUGS L97).
//
// The three instants below are chosen so that layout decides the answer: two
// share a second and one does not.
func SortKeyOrderIsTimeOrder(t *testing.T, newStore Factory) {
	s := newStore(t, "session_log")
	base := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC)
	byTime := []time.Time{
		base,                                  // a whole second
		base.Add(882458214 * time.Nanosecond), // a fraction of the same second
		base.Add(time.Second),                 // the next second
	}
	for i, at := range byTime {
		put(t, s, "session_log", objstore.Key{Partition: tenant, Sort: core.TS(at)},
			map[string]string{"seq": string(rune('0' + i))})
	}
	want := make([]string, len(byTime))
	for i, at := range byTime {
		want[i] = core.TS(at)
	}
	got := s2keys(t, s, objstore.Query{Entity: "session_log", Partition: tenant})
	if !sameOrder(got, want) {
		t.Errorf("ascending read = %v, want %v", got, want)
	}
	got = s2keys(t, s, objstore.Query{Entity: "session_log", Partition: tenant, Descending: true})
	if !sameOrder(got, reversed(want)) {
		t.Errorf("descending read = %v, want %v", got, reversed(want))
	}
	if limited := s2keys(t, s, objstore.Query{
		Entity: "session_log", Partition: tenant, Descending: true, Limit: 1,
	}); len(limited) != 1 || limited[0] != want[len(want)-1] {
		t.Errorf("the newest row is %v, want %q", limited, want[len(want)-1])
	}
}

// ProjectionLeavesUnprojectedAttributesInTheEngine is authd's
// SigningKeyMetadata, whose SELECT list IS its security boundary: it answers
// the operator's question about a signing key and priv_pem never leaves the
// engine. A driver that read the whole item and projected afterwards would put
// the private half on the wire on every call.
func ProjectionLeavesUnprojectedAttributesInTheEngine(t *testing.T, newStore Factory) {
	s := newStore(t, "signing_keys")
	put(t, s, "signing_keys", objstore.Key{Partition: tenant, Sort: "kid-1"},
		map[string]string{"priv_pem": "SECRET", "pub_pem": "public", "active": "1"})
	items, err := s.Query(context.Background(), objstore.Query{
		Entity: "signing_keys", Partition: tenant, Project: []string{"pub_pem", "active", "retired_at"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("read %d keys, want 1", len(items))
	}
	if _, leaked := items[0].Attrs["priv_pem"]; leaked {
		t.Error("a projection that did not name priv_pem returned it")
	}
	if items[0].Attrs["pub_pem"] != "public" || items[0].Attrs["active"] != "1" {
		t.Errorf("the projection dropped what it named: %v", items[0].Attrs)
	}
	if _, present := items[0].Attrs["retired_at"]; present {
		t.Errorf("an attribute the item does not carry came back present: %v", items[0].Attrs)
	}
}

// BatchRefusesWhatNoDriverMayAccept pins the two bounds a batch carries because
// the WEAKEST engine carries them: at most one action per item, and at most
// MaxBatchActions actions. A SQL driver could run both happily; a caller that
// relied on it would break on the engine nobody tested (spec 6/40).
//
// These come back as ERRORS, not refusals. A refused batch is a condition that
// did not hold; a batch shaped like this is a programming mistake, and the two
// must not look alike.
func BatchRefusesWhatNoDriverMayAccept(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	key := objstore.Key{Partition: tenant, Sort: "tok/live"}
	put(t, s, tokens, key, map[string]string{"family": "f1"})

	if _, err := s.GuardedBatch(ctx, nil, []objstore.Write{claim(key), claim(key)}); err == nil {
		t.Error("a batch acting twice on one item was accepted")
	}
	if _, err := s.GuardedBatch(ctx,
		[]objstore.Check{{Entity: tokens, Key: key,
			Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: "family", Op: objstore.Eq, Value: "f1"}}}}},
		[]objstore.Write{claim(key)}); err == nil {
		t.Error("a batch that checked the item it writes was accepted")
	}
	big := make([]objstore.Write, objstore.MaxBatchActions+1)
	for i := range big {
		big[i] = objstore.Write{Op: objstore.Put, Entity: tokens,
			Key:   objstore.Key{Partition: tenant, Sort: "bulk/" + core.TS(time.Unix(int64(i), 0))},
			Attrs: map[string]string{"family": "f1"}}
	}
	if _, err := s.GuardedBatch(ctx, nil, big); err == nil {
		t.Errorf("a batch of %d actions was accepted", len(big))
	}
	if _, err := s.GuardedBatch(ctx, nil, nil); err == nil {
		t.Error("a batch with no writes was accepted")
	}
}

// WriteSemanticsAreTheSameOnEveryDriver pins the four places the engines would
// otherwise answer differently, because each one is a place a driver could
// drift and no caller would notice until it ran on the other engine.
//
// Update requires the item to exist. SQL's UPDATE touches nothing when the row
// is missing and DynamoDB's UpdateItem CREATES it, so the interface picks the
// one both can state exactly.
//
// Put REPLACES the document and Update MERGES into it. A Put that quietly
// merged would leave a tombstone on an item a caller meant to reset.
//
// A delete with no condition is idempotent, which is what a delete means on
// every engine; a delete that asks for presence refuses when there is nothing
// there.
func WriteSemanticsAreTheSameOnEveryDriver(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, tokens)
	missing := objstore.Key{Partition: tenant, Sort: "tok/absent"}
	live := objstore.Key{Partition: tenant, Sort: "tok/live"}
	put(t, s, tokens, live, map[string]string{"family": "f1", "sub": "alice"})

	if ok := commit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: tokens, Key: missing,
		Attrs: map[string]string{"used_at": core.NowTS()}}); ok {
		t.Error("an update of a missing item committed; Update always requires the item to exist")
	}
	if ok := commit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: live,
		Attrs: map[string]string{"family": "f2"}, Cond: objstore.Cond{Item: objstore.ItemAbsent}}); ok {
		t.Error("a create-if-absent overwrote an item that was there")
	}
	if got := one(t, s, tokens, live); got.Attrs["family"] != "f1" {
		t.Errorf("the refused create changed the item: %v", got.Attrs)
	}

	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: tokens, Key: live,
		Attrs: map[string]string{"used_at": "T"}})
	if got := one(t, s, tokens, live); got.Attrs["sub"] != "alice" || got.Attrs["used_at"] != "T" {
		t.Errorf("Update did not merge: %v", got.Attrs)
	}
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Put, Entity: tokens, Key: live,
		Attrs: map[string]string{"family": "f3"}})
	if got := one(t, s, tokens, live); len(got.Attrs) != 1 || got.Attrs["family"] != "f3" {
		t.Errorf("Put did not replace the document: %v", got.Attrs)
	}

	if ok := commit(t, s, ctx, objstore.Write{Op: objstore.Delete, Entity: tokens, Key: missing}); !ok {
		t.Error("deleting a missing item was refused; a delete with no condition is idempotent")
	}
	if ok := commit(t, s, ctx, objstore.Write{Op: objstore.Delete, Entity: tokens, Key: missing,
		Cond: objstore.Cond{Item: objstore.ItemPresent}}); ok {
		t.Error("deleting a missing item committed while asking for its presence")
	}
	mustCommit(t, s, ctx, objstore.Write{Op: objstore.Delete, Entity: tokens, Key: live})
	if items := point(t, s, tokens, live); len(items) != 0 {
		t.Errorf("the deleted item is still there: %v", items)
	}
}

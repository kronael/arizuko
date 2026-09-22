package conformance

// protocols.go holds the bodies for what the two primitives COMPOSE into: the
// versioned replace, its read twin, and the reachability guard (spec 6/40
// §"The seam is TWO primitives, and the rest are protocols").
//
// A protocol is a package function over Query and GuardedBatch, so a driver
// never implements one — but every driver still has to make one WORK, and the
// engines disagree about exactly the thing the three rest on: SQLite serialises
// writers and Postgres at READ COMMITTED does not. So the bodies live here with
// the rest, and each runs against both.
//
// EVERY INTERLEAVE HERE IS FORCED, not raced. Two concurrency bodies in this
// project stayed green against deliberately broken code because a collision has
// to fall inside one window and usually does not. slip (helpers.go) puts the
// other writer in that window on purpose, which is what makes a mutation of the
// compare-and-swap turn a body red every single run.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// markers is the entity the version markers live in — their OWN entity, never
// the one they version. A marker is an item, so a scan of the rows it versions
// would return it as a row with no attributes the caller recognises.
const markers = "scope_markers"

// config is the scope's rows: resreg's registered resource rows, the set
// Apply replaces per subsystem.
const config = "resource_rows"

// graph is the edge entity: acl_membership, keyed by the pair so that a node's
// parents are a prefix read and nothing else.
const graph = "acl_membership"

// errBody is what a test body returns to fail a replace on purpose.
var errBody = errors.New("the body gave up")

// AVersionedReplaceIsFencedByItsMarker is resreg.Apply's shape: make a scope's
// rows equal a document, and version the result.
//
// It cannot be one batch. The routd subsystem's registered tables hold 417 rows
// on krons today and a replace deletes and re-inserts each of them, against
// MaxBatchActions of 100, so the writes span several transactions on every
// engine — and what keeps two replaces apart is the claim on the marker rather
// than a lock. The body seeds and replaces a row set larger than one transaction
// holds, so the chunking is exercised rather than assumed.
//
// What the protocol does NOT give is Apply's pre-image rollback (BUGS L44): a
// replace that fails after writing leaves the scope part-written and its claim
// held, and the assertions below say so rather than hiding it.
func AVersionedReplaceIsFencedByItsMarker(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, markers, config)
	sc := scope("routd")
	ensure(t, s, sc)

	was := scopeRows("was", 150)
	putAll(t, s, config, was)
	v0 := markerAttr(t, s, sc, "version")

	now := scopeRows("now", 150)
	v1, err := objstore.Replace(ctx, s, sc, v0, work("the document"), func(ctx context.Context, r *objstore.Replacer) error {
		// The scope is read UNDER the claim, which is what keeps a second
		// replace's rows out of this one's delete set.
		held := s2keys(t, s, objstore.Query{Entity: config, Partition: tenant})
		if _, err := r.Write(ctx, deletes(held)...); err != nil {
			return err
		}
		_, err := r.Write(ctx, puts(now)...)
		return err
	})
	if err != nil {
		t.Fatalf("the replace failed: %v", err)
	}
	if got := s2keys(t, s, objstore.Query{Entity: config, Partition: tenant}); !sameKeys(
		got, keysOf(now)) {
		t.Errorf("the scope holds %d rows, want the document's %d", len(got), len(now))
	}
	if v1 == v0 {
		t.Errorf("the scope's version is still %q; a replace that does not version its "+
			"result leaves every later reader unable to tell that it ran", v0)
	}
	if h := markerAttr(t, s, sc, "holder"); h != "" {
		t.Errorf("the finished replace left its claim %q on the scope, so no other replace "+
			"can ever start", h)
	}

	// The version is the operator's optimistic guard: resreg.Apply refuses a
	// manifest planned against a checksum the database has moved past, and this
	// is that refusal in the marker's terms.
	ran := false
	if _, err := objstore.Replace(ctx, s, sc, v0, work("the document"), func(context.Context, *objstore.Replacer) error {
		ran = true
		return nil
	}); !errors.Is(err, objstore.ErrScopeMoved) {
		t.Errorf("a replace planned against a version the scope has moved past returned %v, "+
			"want ErrScopeMoved", err)
	}
	if ran {
		t.Error("the stale replace ran its body, so it read a scope it was already too late for")
	}

	// A second replace cannot start while one holds the claim. FORCED: the
	// second runs inside the first's body, so the interleave is certain.
	var inner error
	if _, err := objstore.Replace(ctx, s, sc, "", work("the document"), func(ctx context.Context, r *objstore.Replacer) error {
		_, inner = objstore.Replace(ctx, s, sc, "", work("another document"), func(context.Context, *objstore.Replacer) error {
			t.Error("a second replace ran its body while the first held the scope; two of " +
				"them interleaving their writes is the corruption the claim exists to stop")
			return nil
		})
		return nil
	}); err != nil {
		t.Fatalf("the outer replace failed: %v", err)
	}
	if !errors.Is(inner, objstore.ErrScopeHeld) {
		t.Errorf("a replace against a held scope returned %v, want ErrScopeHeld", inner)
	}

	// The FENCE. A claim taken away part-way stops every write that follows,
	// because each batch carries a check on the marker. Nothing takes a claim
	// today; this is what a sweeper would do to an abandoned one.
	_, err = objstore.Replace(ctx, s, sc, "", work("the document"), func(ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("kept", 1))...); werr != nil {
			return werr
		}
		mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: markers, Key: sc.Key,
			Attrs: map[string]string{"holder": "a sweeper"}})
		if _, werr := r.Write(ctx, puts(scopeRows("lost", 1))...); !errors.Is(
			werr, objstore.ErrClaimLost) {
			t.Errorf("a write under a claim that was taken returned %v, want ErrClaimLost", werr)
		}
		return errBody
	})
	if !errors.Is(err, errBody) {
		t.Errorf("the replace reported %v, want the body's own error", err)
	}
	rows := s2keys(t, s, objstore.Query{Entity: config, Partition: tenant,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: "lost", Sep: "/"}})
	if len(rows) != 0 {
		t.Errorf("the writes after the claim was taken landed anyway: %v; the fence is what "+
			"stops two replaces writing the same scope", rows)
	}
	if h := markerAttr(t, s, sc, "holder"); h != "a sweeper" {
		t.Errorf("the scope's holder is %q, want the claim that took it — a replace that "+
			"wrote and then failed must not settle a scope it left part-written", h)
	}

	// A replace whose claim was TAKEN cannot settle the scope, even when its body
	// reports success: the finish is a compare-and-swap on the claim, so the
	// caller is told rather than the scope quietly declared correct.
	taken := scope("slakd")
	ensure(t, s, taken)
	if _, err := objstore.Replace(ctx, s, taken, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("early", 1))...); werr != nil {
			return werr
		}
		mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: markers,
			Key: taken.Key, Attrs: map[string]string{"holder": "a sweeper"}})
		return nil
	}); !errors.Is(err, objstore.ErrClaimLost) {
		t.Errorf("a replace that ran to the end without its claim returned %v, want "+
			"ErrClaimLost", err)
	}
	if h := markerAttr(t, s, taken, "holder"); h != "a sweeper" {
		t.Errorf("the finish cleared the claim %q it did not hold, so a scope two replaces "+
			"were writing at once now reads as settled", h)
	}

	// A body that fails AFTER writing KEEPS the claim, with nobody stealing it.
	// The scope is part-written, and a released one would read as settled and
	// correct — the silent half of not having Apply's pre-image rollback (L44).
	partial := scope("timed")
	ensure(t, s, partial)
	if _, err := objstore.Replace(ctx, s, partial, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("half", 2))...); werr != nil {
			return werr
		}
		return errBody
	}); !errors.Is(err, errBody) {
		t.Errorf("the replace reported %v, want the body's own error", err)
	}
	if h := markerAttr(t, s, partial, "holder"); h == "" {
		t.Error("a replace that wrote and then failed gave its claim back, so a scope left " +
			"part-written now reads as settled and correct")
	}
	if _, err := objstore.Replace(ctx, s, partial, "", work("another document"), func(
		context.Context, *objstore.Replacer) error {
		t.Error("a replace started on a scope an abandoned claim still holds")
		return nil
	}); !errors.Is(err, objstore.ErrScopeHeld) {
		t.Errorf("a replace of an abandoned scope returned %v, want ErrScopeHeld", err)
	}

	// A body that fails BEFORE writing gives the claim back: nothing changed, so
	// wedging the scope would be a fault this call invented.
	clean := scope("dashd")
	ensure(t, s, clean)
	if _, err := objstore.Replace(ctx, s, clean, "", work("the document"), func(
		context.Context, *objstore.Replacer) error {
		return errBody
	}); !errors.Is(err, errBody) {
		t.Errorf("the replace reported %v, want the body's own error", err)
	}
	if h := markerAttr(t, s, clean, "holder"); h != "" {
		t.Errorf("a replace that failed before writing left the claim %q, so the scope is "+
			"wedged and nothing changed to wedge it for", h)
	}

	// One call names each item at most once, and the rule is enforced over the
	// WHOLE set before any batching. A delete and the insert that replaces the
	// same coordinate 150 writes later fall in DIFFERENT batches, so a driver
	// checking one batch at a time would let the pair through and the caller
	// would be told nothing.
	if _, err := objstore.Replace(ctx, s, clean, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		rows := scopeRows("acl", 150)
		both := append(deletes(keysOf(rows)), puts(rows)...)
		if _, werr := r.Write(ctx, both...); werr == nil {
			t.Errorf("one call took %d deletes and the %d inserts that replace the same "+
				"items, and was accepted; whether a caller is right would then depend on "+
				"where the batch boundary happens to fall", len(rows), len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("the replace failed: %v", err)
	}

	// The claim is a COMPARE-AND-SWAP, and this is the interleave that proves
	// it: a replace reads a settled marker, another one claims the scope, and
	// only then does the first write its own claim. FORCED, because a claim that
	// merely overwrites passes every single-caller assertion above.
	race := scope("emaid")
	ensure(t, s, race)
	const other = "another replace"
	raced := &slip{Store: s, on: markers, left: 1, at: func() {
		mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: markers, Key: race.Key,
			Attrs: map[string]string{"holder": other, "version": "claimed by " + other}})
	}}
	if _, err := objstore.Replace(ctx, raced, race, "", work("the document"), func(
		context.Context, *objstore.Replacer) error {
		t.Error("a replace ran its body after another one claimed the scope between its " +
			"marker read and its own claim")
		return nil
	}); !errors.Is(err, objstore.ErrScopeHeld) {
		t.Errorf("a replace that lost the claim race returned %v, want ErrScopeHeld", err)
	}
	if h := markerAttr(t, s, race, "holder"); h != other {
		t.Errorf("the scope's holder is %q, want %q: the losing claim wrote over the "+
			"winner's, so two replaces now believe they hold the same scope", h, other)
	}

	// A scope whose marker is not there yet is CREATED by the replace, and the
	// engine's own uniqueness decides that race. Only the read protocols refuse
	// a missing marker.
	fresh := scope("onbod")
	if _, err := objstore.Replace(ctx, s, fresh, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		_, werr := r.Write(ctx, puts(scopeRows("new", 2))...)
		return werr
	}); err != nil {
		t.Fatalf("a replace of a scope with no marker failed: %v", err)
	}
	if h := markerAttr(t, s, fresh, "holder"); h != "" {
		t.Errorf("the replace of a fresh scope left the claim %q", h)
	}
}

// AnAbandonedClaimIsFinishedByTheSameWork is the recovery the body above says
// the protocol has and BUGS L267 found it did not: a replace that died after
// writing held its scope for good, so one failed `arizuko apply` disabled the
// config lane until an operator edited the marker by hand.
//
// The claim comes back one way — somebody FINISHES the work — and the proof it
// is the same work is the intent recorded beside the fencing token. That is a
// claim about the DOCUMENT, never about the dead replace's process: a slow
// replace on another host and a dead one look identical, so nothing here reads
// `since` and decides.
//
// EVERY INTERLEAVE IS FORCED. The takeover's compare-and-swap is proved by slip
// putting a third claim between the recovering replace's marker read and its own
// claim write, and the fence by running the takeover INSIDE the body it fences.
// A body that raced for either would pass against a takeover that simply
// overwrote whatever it found.
func AnAbandonedClaimIsFinishedByTheSameWork(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, markers, config)
	all := func(prefix string) []string {
		t.Helper()
		return s2keys(t, s, objstore.Query{Entity: config, Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: prefix, Sep: "/"}})
	}

	// A replace dies after writing PART of its document, which is the state an
	// interrupted apply leaves behind.
	sc := scope("routd")
	ensure(t, s, sc)
	document := scopeRows("now", 150)
	if _, err := objstore.Replace(ctx, s, sc, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(document[:60])...); werr != nil {
			return werr
		}
		return errBody
	}); !errors.Is(err, errBody) {
		t.Fatalf("the replace reported %v, want the body's own error", err)
	}
	if markerAttr(t, s, sc, "holder") == "" {
		t.Fatal("the replace that wrote and then failed gave its claim back, so there is " +
			"no abandoned claim here to recover from")
	}

	// A DIFFERENT document may NOT finish it, and this is the assertion a wrong
	// fix turns red: its own delete set covers the rows it names and nothing
	// else, so settling the scope here would declare correct a scope still
	// part-written outside it.
	if _, err := objstore.Replace(ctx, s, sc, "", work("another document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		_, werr := r.Write(ctx, puts(scopeRows("other", 3))...)
		return werr
	}); !errors.Is(err, objstore.ErrScopeHeld) {
		t.Errorf("a replace of another document over a part-written scope returned %v, "+
			"want ErrScopeHeld", err)
	}
	if h := markerAttr(t, s, sc, "holder"); h == "" {
		t.Error("a scope 60 rows into a 150-row document now reads as SETTLED, because a " +
			"replace that cannot prove it writes the same rows released the claim anyway")
	}
	if got := all("other"); len(got) != 0 {
		t.Errorf("the refused replace left %v behind", got)
	}

	// The SAME work finishes it. Its writes span several batches, so the claim it
	// took over carries the whole roll-forward rather than the first chunk.
	if _, err := objstore.Replace(ctx, s, sc, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, deletes(all("now"))...); werr != nil {
			return werr
		}
		_, werr := r.Write(ctx, puts(document)...)
		return werr
	}); err != nil {
		t.Fatalf("the same work could not finish the claim it left behind: %v; one failed "+
			"replace then holds the scope for good", err)
	}
	if got := all("now"); !sameKeys(got, keysOf(document)) {
		t.Errorf("the rolled-forward scope holds %d rows, want the document's %d",
			len(got), len(document))
	}
	if h := markerAttr(t, s, sc, "holder"); h != "" {
		t.Errorf("the roll-forward left the claim %q on the scope", h)
	}

	// The replace whose claim is taken over is FENCED, which is what makes a
	// takeover safe against a holder that is not dead after all. FORCED: the
	// takeover runs inside the body it fences.
	fenced := scope("onbod")
	ensure(t, s, fenced)
	var inner, late error
	if _, err := objstore.Replace(ctx, s, fenced, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("first", 2))...); werr != nil {
			return werr
		}
		_, inner = objstore.Replace(ctx, s, fenced, "", work("the document"), func(
			ctx context.Context, r2 *objstore.Replacer) error {
			_, werr := r2.Write(ctx, puts(scopeRows("second", 2))...)
			return werr
		})
		_, late = r.Write(ctx, puts(scopeRows("late", 2))...)
		return nil
	}); !errors.Is(err, objstore.ErrClaimLost) {
		t.Errorf("the replace whose claim was taken over returned %v, want ErrClaimLost", err)
	}
	if inner != nil {
		t.Errorf("the takeover of the same work failed: %v", inner)
	}
	if !errors.Is(late, objstore.ErrClaimLost) {
		t.Errorf("a write under a claim another replace took over returned %v, want "+
			"ErrClaimLost", late)
	}
	if got := all("late"); len(got) != 0 {
		t.Errorf("the writes of the fenced replace landed anyway: %v", got)
	}

	// The takeover is a COMPARE-AND-SWAP. FORCED: a third claim lands between the
	// recovering replace's marker read and its own claim write, and it carries
	// the SAME intent, so only the swap can stop it.
	wedged := scope("timed")
	ensure(t, s, wedged)
	if _, err := objstore.Replace(ctx, s, wedged, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("wedged", 2))...); werr != nil {
			return werr
		}
		return errBody
	}); !errors.Is(err, errBody) {
		t.Fatalf("the replace reported %v, want the body's own error", err)
	}
	fired := 0
	const rival = "another recovery"
	slipped := SlipAt(s, markers, 1, func() {
		fired++
		mustCommit(t, s, ctx, objstore.Write{Op: objstore.Update, Entity: markers,
			Key: wedged.Key, Attrs: map[string]string{"holder": rival,
				"intent": "the document", "version": "claimed by " + rival}})
	})
	if _, err := objstore.Replace(ctx, slipped, wedged, "", work("the document"), func(
		context.Context, *objstore.Replacer) error {
		t.Error("a replace ran its body after another one took the claim between its " +
			"marker read and its own claim")
		return nil
	}); !errors.Is(err, objstore.ErrScopeHeld) {
		t.Errorf("a takeover that lost the claim race returned %v, want ErrScopeHeld", err)
	}
	if fired != 1 {
		t.Fatalf("the interleave fired %d times, so this proved nothing about the swap", fired)
	}
	if h := markerAttr(t, s, wedged, "holder"); h != rival {
		t.Errorf("the scope's holder is %q, want %q: the losing takeover wrote over the "+
			"winner's claim, so two replaces are recovering the same part-written scope "+
			"at once", h, rival)
	}

	// Claim.Take is the operator's own assertion, and the ONLY way a document
	// that cannot prove it covers the interrupted work may finish it.
	adopted := scope("slakd")
	ensure(t, s, adopted)
	if _, err := objstore.Replace(ctx, s, adopted, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		if _, werr := r.Write(ctx, puts(scopeRows("adopted", 2))...); werr != nil {
			return werr
		}
		return errBody
	}); !errors.Is(err, errBody) {
		t.Fatalf("the replace reported %v, want the body's own error", err)
	}
	if _, err := objstore.Replace(ctx, s, adopted, "",
		objstore.Claim{Intent: "another document", Take: true},
		func(ctx context.Context, r *objstore.Replacer) error {
			_, werr := r.Write(ctx, puts(scopeRows("operator", 2))...)
			return werr
		}); err != nil {
		t.Errorf("a claim the operator took over returned %v, so a scope whose interrupted "+
			"work nobody can reproduce has no escape at all", err)
	}
	if h := markerAttr(t, s, adopted, "holder"); h != "" {
		t.Errorf("the operator's takeover left the claim %q on the scope", h)
	}

	// A claim with no intent is refused, because the claim it would leave behind
	// could never be proved to be its own work.
	if _, err := objstore.Replace(ctx, s, scope("dashd"), "", objstore.Claim{},
		func(context.Context, *objstore.Replacer) error {
			t.Error("a replace with no intent ran its body")
			return nil
		}); err == nil {
		t.Error("a replace with no intent was accepted")
	}
}

// AConsistentMultiReadRetriesWhenTheScopeMovesUnderIt is
// resreg.ExportSnapshot's shape: N reads that have to describe ONE state.
//
// resreg wraps them in a read transaction today (resreg/archive.go:69), and
// that cannot go on the interface. DynamoDB's only serializable multi-item read
// is TransactGetItems and each slot in it is a GetItem by primary key, so a
// Query — every scoped read arizuko makes — cannot go in one, and Query and
// Scan are read-committed with nothing beside them.
// https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactGetItems.html
//
// So it is a protocol: read the marker, run the queries, read the marker again.
// The interleave is FORCED — a writer commits in the window between the first
// marker read and the caller's queries — because a body that hoped to land
// there would pass against a protocol that never re-read the marker at all.
func AConsistentMultiReadRetriesWhenTheScopeMovesUnderIt(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, markers, config)
	sc := scope("routd")
	ensure(t, s, sc)
	putAll(t, s, config, scopeRows("acl", 3))

	attempts := 0
	var seen []string
	slipped := &slip{Store: s, left: 1, at: func() {
		mustCommit(t, s, ctx, objstore.Bump(sc))
	}}
	if err := objstore.ReadConsistent(ctx, slipped, sc, func(ctx context.Context) error {
		attempts++
		// A retry re-runs this, so what it accumulates it discards first.
		seen = s2keys(t, slipped, objstore.Query{Entity: config, Partition: tenant})
		return nil
	}); err != nil {
		t.Fatalf("the consistent read failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("the read ran %d times, want 2: a writer committed between the first marker "+
			"read and the queries, and a read that does not run again reported a state no "+
			"single writer ever saw", attempts)
	}
	if !sameKeys(seen, keysOf(scopeRows("acl", 3))) {
		t.Errorf("the read returned %v, want the scope's three rows", seen)
	}

	// A scope with no marker is refused rather than reported consistent: no
	// writer bumps a marker that is not there, so "unchanged" over an absent one
	// is a guarantee nobody made.
	if err := objstore.ReadConsistent(ctx, s, scope("nobody"), func(context.Context) error {
		t.Error("a consistent read ran its queries over a scope with no marker")
		return nil
	}); !errors.Is(err, objstore.ErrNoMarker) {
		t.Errorf("a consistent read of a scope with no marker returned %v, want ErrNoMarker", err)
	}

	// A scope a replace HOLDS is refused, and equal versions are why: the claim
	// bumps the version once at the start and once at the finish, so a replace
	// that claimed before the first read and settles after the second shows the
	// same version at both — with every one of its row writes in between.
	var held error
	if _, err := objstore.Replace(ctx, s, sc, "", work("the document"), func(
		ctx context.Context, r *objstore.Replacer) error {
		held = objstore.ReadConsistent(ctx, s, sc, func(context.Context) error {
			t.Error("a consistent read ran its queries across a scope being replaced")
			return nil
		})
		return nil
	}); err != nil {
		t.Fatalf("the replace failed: %v", err)
	}
	if !errors.Is(held, objstore.ErrScopeHeld) {
		t.Errorf("a consistent read of a held scope returned %v, want ErrScopeHeld", held)
	}

	// The protocol detects exactly the writers that carry Bump, and no others.
	// That is a discipline rather than a guarantee the engine gives, and it is
	// stated here as an assertion so that nobody reads more into the protocol
	// than it does.
	quiet := &slip{Store: s, left: 1, at: func() {
		put(t, s, config, objstore.Key{Partition: tenant, Sort: "acl/unmarked"},
			map[string]string{"seq": "1"})
	}}
	if err := objstore.ReadConsistent(ctx, quiet, sc, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("a write that carried no Bump was reported as an interleave: %v", err)
	}

	// A scope that moves under every attempt gives up and says so. FORCED: every
	// read fires a bump, so the protocol never sees two equal versions.
	busy := &slip{Store: s, left: 1000, at: func() {
		mustCommit(t, s, ctx, objstore.Bump(sc))
	}}
	if err := objstore.ReadConsistent(ctx, busy, sc, func(context.Context) error {
		return nil
	}); err == nil {
		t.Error("a read of a scope that moved under every attempt reported success")
	}

	// An error from the caller's own read is the caller's, returned as it is.
	if err := objstore.ReadConsistent(ctx, s, sc, func(context.Context) error {
		return errBody
	}); !errors.Is(err, errBody) {
		t.Errorf("the consistent read returned %v, want the caller's own error", err)
	}
}

// AnEdgeThatWouldCloseACycleIsRefusedFromBothEnds is store.AddMembership's
// invariant, and BUGS L256 is why it is a protocol rather than a Check.
//
// The walk is a graph reachability test decided by an unbounded read.
// objstore.Check binds an ITEM — this key, this condition — and a frontier is
// not an item, so two concurrent inserts closing a cycle from opposite ends
// each walk a graph without the other's edge, both pass, and both commit.
// SQLite's single writer hides it; Postgres at READ COMMITTED does not.
//
// The last half of this body is that exact pair, FORCED with slip so the second
// walker reads the graph before the first commits. A version guard removed from
// the batch turns it red every run rather than three times in twelve.
func AnEdgeThatWouldCloseACycleIsRefusedFromBothEnds(t *testing.T, newStore Factory) {
	ctx := context.Background()
	s := newStore(t, markers, graph)
	sc := scope("acl")
	ensure(t, s, sc)
	up := parentsIn(s)

	// b is a member of a.
	if ok, err := objstore.AddEdge(ctx, s, sc, up, "b", "a", edge("b", "a")); err != nil || !ok {
		t.Fatalf("the first edge reported ok=%v err=%v", ok, err)
	}
	// a therefore cannot be a member of b.
	if _, err := objstore.AddEdge(ctx, s, sc, up, "a", "b", edge("a", "b")); !errors.Is(
		err, objstore.ErrCycle) {
		t.Errorf("the edge that closes a two-node cycle returned %v, want ErrCycle", err)
	}
	if got := edges(t, s); !sameOrder(got, []string{"b/a"}) {
		t.Errorf("the graph is %v, want only b/a — a refused edge writes nothing", got)
	}

	// The walk is TRANSITIVE, not one hop: c is a member of b, so a cannot be a
	// member of c.
	if ok, err := objstore.AddEdge(ctx, s, sc, up, "c", "b", edge("c", "b")); err != nil || !ok {
		t.Fatalf("the second edge reported ok=%v err=%v", ok, err)
	}
	if _, err := objstore.AddEdge(ctx, s, sc, up, "a", "c", edge("a", "c")); !errors.Is(
		err, objstore.ErrCycle) {
		t.Errorf("the edge that closes a three-node cycle returned %v, want ErrCycle: a walk "+
			"that stops at the direct parents finds no cycle deeper than one hop", err)
	}

	// A node cannot be its own parent. The walk seeds with the node it starts
	// from, which is the `SELECT ?` seed of the recursive query both copies run,
	// so a cycle of length one needs no separate test.
	if _, err := objstore.AddEdge(ctx, s, sc, up, "d", "d", edge("d", "d")); !errors.Is(
		err, objstore.ErrCycle) {
		t.Errorf("a self edge returned %v, want ErrCycle", err)
	}

	// The caller's own condition still refuses in its own terms: an edge already
	// there is a plain refusal, not a cycle and not a fault.
	if ok, err := objstore.AddEdge(ctx, s, sc, up, "b", "a", edge("b", "a")); err != nil || ok {
		t.Errorf("re-adding an edge reported ok=%v err=%v, want a refusal with no error", ok, err)
	}

	// THE POINT — BUGS L256. Two inserts close a cycle from opposite ends, and
	// both walk a graph without the other's edge.
	g := newStore(t, markers, graph)
	gsc := scope("acl")
	ensure(t, g, gsc)
	var firstOK bool
	var firstErr error
	// The second walker reads the graph, and only THEN does the first commit its
	// whole edge. So the second decides on a graph with no cycle in it and has
	// nothing but the marker to catch it.
	second := &slip{Store: g, on: graph, left: 1, at: func() {
		firstOK, firstErr = objstore.AddEdge(ctx, g, gsc, parentsIn(g), "b", "a", edge("b", "a"))
	}}
	ok, err := objstore.AddEdge(ctx, second, gsc, parentsIn(second), "a", "b", edge("a", "b"))
	if firstErr != nil || !firstOK {
		t.Fatalf("the first insert reported ok=%v err=%v", firstOK, firstErr)
	}
	if !errors.Is(err, objstore.ErrCycle) {
		t.Errorf("the second insert reported ok=%v err=%v, want ErrCycle: it walked a graph "+
			"without the first one's edge, so only the marker it compare-and-swaps can make "+
			"it walk again (BUGS L256)", ok, err)
	}
	if got := edges(t, g); !sameOrder(got, []string{"b/a"}) {
		t.Errorf("the graph is %v, want only b/a — two edges is the cycle both walkers were "+
			"allowed to close", got)
	}

	// A graph with no marker has nothing for two inserts to serialise on, so it
	// is refused rather than walked.
	if _, err := objstore.AddEdge(ctx, s, scope("unmarked"), up, "e", "f",
		edge("e", "f")); !errors.Is(err, objstore.ErrNoMarker) {
		t.Errorf("an edge against a scope with no marker returned %v, want ErrNoMarker", err)
	}
}

// scope names one version marker, in the entity markers live in.
func scope(name string) objstore.Scope {
	return objstore.Scope{Entity: markers, Key: objstore.Key{Partition: tenant, Sort: name}}
}

// work is a claim naming what a replace will write. Two replaces that would
// make the scope equal the same document carry the same value, and that is the
// whole proof one of them may finish what the other left (objstore.Claim).
func work(intent string) objstore.Claim { return objstore.Claim{Intent: intent} }

// ensure creates a scope's marker.
func ensure(t *testing.T, s objstore.Store, sc objstore.Scope) {
	t.Helper()
	if err := objstore.EnsureScope(context.Background(), s, sc); err != nil {
		t.Fatalf("ensure the marker for %s: %v", sc.Key.Sort, err)
	}
}

// markerAttr reads one attribute off a scope's marker, and reports an absent
// one as the empty string — which is what absent means to every caller here.
func markerAttr(t *testing.T, s objstore.Store, sc objstore.Scope, attr string) string {
	t.Helper()
	return one(t, s, sc.Entity, sc.Key).Attrs[attr]
}

// scopeRows renders n rows of a scope, under one prefix.
func scopeRows(prefix string, n int) []objstore.Item {
	out := make([]objstore.Item, n)
	for i := range out {
		out[i] = objstore.Item{
			Key:   objstore.Key{Partition: tenant, Sort: fmt.Sprintf("%s/%03d", prefix, i)},
			Attrs: map[string]string{"seq": fmt.Sprintf("%d", i)},
		}
	}
	return out
}

// puts renders the inserts half of a replace.
func puts(items []objstore.Item) []objstore.Write {
	out := make([]objstore.Write, len(items))
	for i, it := range items {
		out[i] = objstore.Write{Op: objstore.Put, Entity: config, Key: it.Key, Attrs: it.Attrs}
	}
	return out
}

// deletes renders the prune half of a replace, over the keys the scope holds.
func deletes(keys []string) []objstore.Write {
	out := make([]objstore.Write, len(keys))
	for i, k := range keys {
		out[i] = objstore.Write{Op: objstore.Delete, Entity: config,
			Key: objstore.Key{Partition: tenant, Sort: k}}
	}
	return out
}

// edge renders the write one membership edge is. The sort key is the pair, so a
// node's parents are a prefix read and the walk needs no second index. It is
// create-if-absent, so an edge already there refuses in the caller's own terms.
func edge(child, parent string) []objstore.Write {
	return []objstore.Write{{
		Op: objstore.Put, Entity: graph,
		Key:   objstore.Key{Partition: tenant, Sort: child + "/" + parent},
		Attrs: map[string]string{"child": child, "parent": parent},
		Cond:  objstore.Cond{Item: objstore.ItemAbsent},
	}}
}

// parentsIn answers "which nodes sit directly above this one" out of the edge
// entity. The separator is what keeps `b` from reaching `bb/x`.
func parentsIn(s objstore.Store) objstore.ParentsFunc {
	return func(ctx context.Context, node string) ([]string, error) {
		items, err := s.Query(ctx, objstore.Query{
			Entity: graph, Partition: tenant,
			Key:     objstore.KeyPredicate{Op: objstore.PrefixKey, Value: node, Sep: "/"},
			Project: []string{"parent"},
		})
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Attrs["parent"])
		}
		return out, nil
	}
}

// edges lists every edge in the graph, by its pair key.
func edges(t *testing.T, s objstore.Store) []string {
	t.Helper()
	return s2keys(t, s, objstore.Query{Entity: graph, Partition: tenant})
}

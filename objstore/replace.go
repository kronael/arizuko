package objstore

// replace.go is the versioned replace: make a scope's contents equal a
// document, and version the result (spec 6/40 §3).
//
// It cannot be one GuardedBatch and that is not a driver's shortcoming.
// resreg.Apply processes an UNBOUNDED row set: the routd subsystem's registered
// tables hold 417 rows on krons today (acl 42, acl_membership 58, routes 58,
// route_tokens 43, network_rules 15, web_routes 54, proxyd_routes 26, groups 53,
// scheduled_tasks 66, pending_actions 2), and a replace issues a delete AND an
// insert for each one against MaxBatchActions of 100. So the writes span several
// batches whatever the engine is, and the intermediate state is real and
// observable. 6/40 states the shape: Query, a start-marker CAS, N guarded
// writes, a finish CAS.
//
// ONE DOMAIN CONSUMER, and it is resreg.Apply. store.DeleteGroup is an
// unversioned subtree purge (store/groups.go:113) and does not ask for this.
// One caller is a function, not an interface member.
//
// WHAT ADOPTING THIS COSTS Apply, stated plainly because it is a real loss.
// Apply today runs every DELETE and INSERT inside ONE SQL transaction, so a
// crash or an error part-way rolls the whole subsystem back to its pre-image —
// the rollback BUGS L44 depends on. A marker-gated sequence has no pre-image
// and no rollback. A replace that dies half-way leaves the scope PART-WRITTEN
// with its marker still held, and recovery is to run the replace again
// (roll-forward), never to undo it. That is what buys an unbounded row set, and
// it is the price 6/37's `Apply` redesign costed for a key-value engine.
//
// THE CLAIM IS NOT RELEASED WHEN A BODY FAILS AFTER WRITING, and nothing else
// releases one either. A released half-written scope reads as settled and
// correct, which is the silent corruption; a held one is loud — the next
// Replace answers ErrScopeHeld and names the holder's `since`. So over a scope
// somebody has written, the claim comes back one way only: a replace FINISHES
// the work, which is what roll-forward means and why it is the recovery the
// paragraph above promises. (A body that fails before writing gives its claim
// straight back, because nothing changed to wedge the scope for.)
//
// WHAT LETS A SECOND REPLACE FINISH THE FIRST ONE'S WORK. The claim records the
// holder's INTENT — what it is going to write — beside the fencing token, and a
// replace whose intent equals the held one's takes the claim over. That is safe
// because a replace makes a scope EQUAL a document: run it twice and the second
// run reads the part-written state, deletes it and puts the same document over
// it, landing exactly where a clean run would have. It is a proof about the
// WORK, so it needs no proof about the dead replace's process — a takeover
// fences whatever the old holder had left, so the loser of the two is told
// ErrClaimLost rather than writing into the winner.
//
// LIVENESS IS NOT PROVED AND IS NOT ASKED FOR. `since` is old is not evidence a
// holder is gone: a slow replace on another host looks identical to a dead one,
// and a sweeper acting on a timeout would take the scope from a replace that is
// still writing it. That is why `since` is documented as what an OPERATOR reads
// (marker.go) and never as a predicate this file evaluates. A claim whose work
// nobody can reproduce is therefore the operator's call, and Claim.Take is where
// they make it — an assertion, not an inference.
//
// WHAT A TAKEOVER COSTS is the guard the version check was: the version the
// caller planned against is the one the dead claim already moved, so a takeover
// skips it. There is nothing left to protect — the scope is part-written, so no
// state the caller could have planned against still exists — but the caller is
// told, through Replacer.TookOver, and the checks it built on that same
// assumption drop with it.

import (
	"context"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/core"
)

var (
	// ErrScopeMoved is the answer when the scope's version is not the one the
	// caller planned against.
	//
	// It is resreg.Apply's ErrChecksumMismatch in the marker's terms: the
	// operator planned a change against a document, and the document moved
	// before the change landed. Apply's `force` is an empty wantVersion.
	ErrScopeMoved = errors.New("the scope moved since the caller read it")

	// ErrClaimLost is the answer when the claim this replace holds is no longer
	// on the marker: somebody took the scope while these writes were running.
	//
	// It is not a refusal the caller can act on and it is not transient. The
	// scope is part-written by two callers, so the answer is loud and the
	// remaining writes do not go in.
	ErrClaimLost = errors.New("the replace no longer holds the scope")
)

// Claim says what a replace is about to do, so a replace that dies part-way can
// be finished instead of wedging the scope for good.
type Claim struct {
	// Intent names the WORK, and two replaces that would make the scope equal
	// the same document carry the same value. It is derived from that document,
	// never handed in per call site: a caller that invented a fresh value each
	// run could never take its own claim back, and one that reused a constant
	// would take over work it does not cover. resreg.applyClaim is the
	// derivation, and it is the only one.
	//
	// It is REQUIRED. A replace with no intent leaves a claim nothing can ever
	// prove was its own, and that claim is then the lock BUGS L267 reported.
	Intent string

	// Take adopts a claim whose intent DIFFERS: the operator asserting that the
	// interrupted work is not coming back and that this document is what the
	// scope should hold.
	//
	// It is an operator gesture and never a default, because it gives up the one
	// thing the intent match buys. This document's own delete set covers the
	// rows it names; anything the dead replace was part-way through OUTSIDE that
	// set stays part-written and the scope is then settled over it.
	Take bool
}

// adopts reports a held claim this one may finish.
func (c Claim) adopts(was marker) bool { return c.Take || c.Intent == was.intent }

// Replacer applies one replace's writes under its claim. body receives it and
// nothing else reaches the fence.
type Replacer struct {
	s        Store
	sc       Scope
	token    string
	intent   string
	tookOver bool
	wrote    bool
}

// TookOver reports a claim this replace ADOPTED from one that died part-way,
// which means the scope was already part-written when this body started.
//
// A body reads it to drop the guards that assumed otherwise: resreg.Apply
// compares the scope's checksum against the one the operator planned against,
// and on a takeover that pre-image is exactly what the dead replace already
// began overwriting, so the comparison would refuse the only action that can
// settle the scope.
func (r *Replacer) TookOver() bool { return r.tookOver }

// Replace runs body under an exclusive claim on the scope and versions the
// result. It answers the scope's new version.
//
// wantVersion is the version the caller planned against, and an empty one skips
// the check. body reads the scope UNDER the claim and hands its writes to the
// Replacer: reading inside the claim is what keeps a second replace's rows out
// of this one's delete set. It does not keep a BOUNDED writer out — the claim
// gates replaces, not ordinary writes — which is 6/40's "a row added in between
// survives", now narrowed to the writers that do not take the claim.
//
// A HELD SCOPE is refused unless cl would finish the work that holds it, in
// which case this takes the claim over and rolls forward. Both halves are in
// the file comment; the refusal names what the holder was writing so the
// operator can see which of the two answers applies.
//
// A body that returns an error before writing anything releases the claim,
// because nothing changed and a wedged scope would be a fault this call
// invented. A body that returns an error after writing keeps it, and so does one
// that took a part-written scope over and then failed before writing: releasing
// there would settle a scope the DEAD replace left torn.
func Replace(ctx context.Context, s Store, sc Scope, wantVersion string, cl Claim,
	body func(context.Context, *Replacer) error) (string, error) {
	if cl.Intent == "" {
		return "", fmt.Errorf("replace %s/%s: the claim names no intent, so a claim this "+
			"replace left behind could never be proved to be its own work",
			sc.Entity, sc.Key.Sort)
	}
	was, err := readMarker(ctx, s, sc)
	if err != nil {
		return "", err
	}
	held := was.holder != ""
	if held && !cl.adopts(was) {
		return was.version, fmt.Errorf("replace %s/%s: %w since %s, and it is writing %q "+
			"rather than %q, so finishing it here would settle a scope this replace does "+
			"not cover: run that work again to roll it forward, or take the claim over and "+
			"accept that rows outside this document stay part-written",
			sc.Entity, sc.Key.Sort, ErrScopeHeld, was.since, was.intent, cl.Intent)
	}
	// The version check is the caller's plan against a SETTLED scope, and a held
	// one has already moved past every version a caller could have planned
	// against. See the file comment.
	if !held && wantVersion != "" && wantVersion != was.version {
		return was.version, fmt.Errorf("replace %s/%s: %w: planned against %q, the scope is at %q",
			sc.Entity, sc.Key.Sort, ErrScopeMoved, wantVersion, was.version)
	}
	r := &Replacer{s: s, sc: sc, token: freshVersion(), intent: cl.Intent, tookOver: held}
	if err := r.claim(ctx, was); err != nil {
		return was.version, err
	}
	if err := body(ctx, r); err != nil {
		if r.wrote || r.tookOver {
			return "", fmt.Errorf("replace %s/%s failed over a part-written scope, so its "+
				"claim stays held until the same work is run again: %w",
				sc.Entity, sc.Key.Sort, err)
		}
		if rerr := r.release(ctx); rerr != nil {
			return "", fmt.Errorf("replace %s/%s failed (%w) and its claim could not be "+
				"released: %v", sc.Entity, sc.Key.Sort, err, rerr)
		}
		return "", err
	}
	return r.finish(ctx)
}

// Write applies the writes under the claim, in the order given, in batches no
// larger than one transaction holds.
//
// Every batch carries a Check on the marker, which is the fence: a batch whose
// claim has been taken writes nothing. The Check is an action, so a batch holds
// one write fewer than MaxBatchActions.
//
// The writes go in ORDER, and one call names each item at most once. A replace
// deletes a scope and inserts the document over it, so a delete and the insert
// that replaces the same coordinate are certain — and they are TWO calls, the
// way resreg.Apply already runs DeleteScope and then InsertAll. Handed to one
// call they would collide or not depending on where the batch boundary happened
// to fall, so this refuses them outright instead.
//
// A batch boundary is not atomic: this is the unbounded write, and every batch
// before a refusal has already committed.
//
// committed=false is the caller's own condition refusing, exactly as it is on
// GuardedBatch. A lost fence is ErrClaimLost instead, because a caller cannot
// act on it.
func (r *Replacer) Write(ctx context.Context, writes ...Write) (bool, error) {
	if len(writes) == 0 {
		return true, nil
	}
	if err := oneActionPerItem(writes); err != nil {
		return false, fmt.Errorf("replace %s/%s: %w", r.sc.Entity, r.sc.Key.Sort, err)
	}
	fence := []Check{{Entity: r.sc.Entity, Key: r.sc.Key,
		Cond: Cond{Where: []Cmp{{Attr: markerHolder, Op: Eq, Value: r.token}}}}}
	for len(writes) > 0 {
		n := min(len(writes), MaxBatchActions-len(fence))
		committed, err := r.s.GuardedBatch(ctx, fence, writes[:n])
		if err != nil {
			return false, err
		}
		if !committed {
			// A refusal is one bit, so this call re-reads the marker to say
			// which of the two refused — the same move Increment makes on the
			// path that has already lost (protocol.go).
			held, rerr := readMarker(ctx, r.s, r.sc)
			if rerr != nil {
				return false, rerr
			}
			if held.holder != r.token {
				return false, fmt.Errorf("replace %s/%s: %w, it is now held by %q",
					r.sc.Entity, r.sc.Key.Sort, ErrClaimLost, held.holder)
			}
			return false, nil
		}
		r.wrote = true
		writes = writes[n:]
	}
	return true, nil
}

// claim takes the scope, refusing if anybody else took it first.
//
// The shape follows what the read found. A marker that is there is an Update
// conditioned on the version this call read, which is the compare-and-swap. A
// marker that is not there is a create-if-absent, where the engine's own
// uniqueness decides the race — the substitute sqlstore's refused ItemAbsent
// check names.
//
// The version is the WHOLE condition, and a second term asking `holder` to be
// absent would add nothing: every claim moves the version in the same write it
// sets the holder, so a claim that landed after this call's read has already
// moved the version out from under this condition. Whether a holder is there is
// answered where a caller can act on it — by the read above, which returns
// ErrScopeHeld with the holder's `since`.
//
// A TAKEOVER IS THE SAME WRITE. The condition already says "the marker is
// exactly as I read it", which is what a takeover needs too: a third replace
// that claimed in between moved the version, so this refuses and re-reads. So
// there is no second claim path to drift from this one — only the guard above
// differs, and it is the one that decides whether a held scope may be taken.
func (r *Replacer) claim(ctx context.Context, was marker) error {
	attrs := map[string]string{
		markerVersion: freshVersion(),
		markerHolder:  r.token,
		markerIntent:  r.intent,
		markerSince:   core.NowTS(),
	}
	w := Write{Op: Put, Entity: r.sc.Entity, Key: r.sc.Key, Attrs: attrs,
		Cond: Cond{Item: ItemAbsent}}
	if was.present {
		w = Write{Op: Update, Entity: r.sc.Entity, Key: r.sc.Key, Attrs: attrs,
			Cond: Cond{Where: []Cmp{{Attr: markerVersion, Op: Eq, Value: was.version}}}}
	}
	committed, err := r.s.GuardedBatch(ctx, nil, []Write{w})
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	// The refusal is one bit and the two causes need different answers, so this
	// re-reads the marker rather than guessing which one it was.
	now, err := readMarker(ctx, r.s, r.sc)
	if err != nil {
		return err
	}
	if now.holder != "" {
		return fmt.Errorf("replace %s/%s: %w since %s, writing %q",
			r.sc.Entity, r.sc.Key.Sort, ErrScopeHeld, now.since, now.intent)
	}
	return fmt.Errorf("replace %s/%s: %w: it was at %q when the claim was read and is at %q now",
		r.sc.Entity, r.sc.Key.Sort, ErrScopeMoved, was.version, now.version)
}

// release gives the scope back without changing anything, for a body that
// failed before it wrote on a scope that was settled when it started. It is the
// ONLY call that clears a claim without finishing the work, and Replace is what
// keeps it to that one case.
func (r *Replacer) release(ctx context.Context) error {
	committed, err := r.s.GuardedBatch(ctx, nil, []Write{{
		Op: Update, Entity: r.sc.Entity, Key: r.sc.Key,
		Remove: []string{markerHolder, markerIntent, markerSince},
		Cond:   Cond{Where: []Cmp{{Attr: markerHolder, Op: Eq, Value: r.token}}},
	}})
	if err != nil {
		return err
	}
	if !committed {
		return fmt.Errorf("%w, so it was not released", ErrClaimLost)
	}
	return nil
}

// finish settles the scope and gives it the version every later caller compares
// against. It is a compare-and-swap on the claim: a replace whose claim was
// taken does not get to declare the scope settled.
func (r *Replacer) finish(ctx context.Context) (string, error) {
	version := freshVersion()
	committed, err := r.s.GuardedBatch(ctx, nil, []Write{{
		Op: Update, Entity: r.sc.Entity, Key: r.sc.Key,
		Attrs:  map[string]string{markerVersion: version},
		Remove: []string{markerHolder, markerIntent, markerSince},
		Cond:   Cond{Where: []Cmp{{Attr: markerHolder, Op: Eq, Value: r.token}}},
	}})
	if err != nil {
		return "", err
	}
	if !committed {
		return "", fmt.Errorf("replace %s/%s finished but %w, so the scope stays unsettled",
			r.sc.Entity, r.sc.Key.Sort, ErrClaimLost)
	}
	return version, nil
}

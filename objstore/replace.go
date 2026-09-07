package objstore

// replace.go is the versioned replace: make a scope's contents equal a
// document, and version the result (spec 6/40 §3).
//
// It cannot be one GuardedBatch and that is not a driver's shortcoming.
// resreg.Apply processes an UNBOUNDED row set — 417 rows in one krons apply
// against MaxBatchActions of 100 — so the writes span several batches whatever
// the engine is, and the intermediate state is real and observable. 6/40 states
// the shape: Query, a start-marker CAS, N guarded writes, a finish CAS.
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
// it is the price 6/38 costed for a key-value engine.
//
// The claim is not released when a body fails after writing. A released
// half-written scope reads as settled and correct, which is the silent
// corruption; a held one is loud — the next Replace answers ErrScopeHeld and
// names the holder's `since`. Nothing sweeps an abandoned claim today, so an
// operator clears it.

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

// Replacer applies one replace's writes under its claim. body receives it and
// nothing else reaches the fence.
type Replacer struct {
	s     Store
	sc    Scope
	token string
	wrote bool
}

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
// A body that returns an error before writing anything releases the claim,
// because nothing changed and a wedged scope would be a fault this call
// invented. A body that returns an error after writing keeps it: see the file
// comment.
func Replace(ctx context.Context, s Store, sc Scope, wantVersion string,
	body func(context.Context, *Replacer) error) (string, error) {
	was, err := readMarker(ctx, s, sc)
	if err != nil {
		return "", err
	}
	if was.holder != "" {
		return was.version, fmt.Errorf("replace %s/%s: %w since %s",
			sc.Entity, sc.Key.Sort, ErrScopeHeld, was.since)
	}
	if wantVersion != "" && wantVersion != was.version {
		return was.version, fmt.Errorf("replace %s/%s: %w: planned against %q, the scope is at %q",
			sc.Entity, sc.Key.Sort, ErrScopeMoved, wantVersion, was.version)
	}
	r := &Replacer{s: s, sc: sc, token: freshVersion()}
	if err := r.claim(ctx, was); err != nil {
		return was.version, err
	}
	if err := body(ctx, r); err != nil {
		if r.wrote {
			return "", fmt.Errorf("replace %s/%s wrote and then failed, so the scope is "+
				"part-written and its claim stays held until the replace is run again: %w",
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
func (r *Replacer) claim(ctx context.Context, was marker) error {
	attrs := map[string]string{
		markerVersion: freshVersion(),
		markerHolder:  r.token,
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
		return fmt.Errorf("replace %s/%s: %w since %s",
			r.sc.Entity, r.sc.Key.Sort, ErrScopeHeld, now.since)
	}
	return fmt.Errorf("replace %s/%s: %w: it was at %q when the claim was read and is at %q now",
		r.sc.Entity, r.sc.Key.Sort, ErrScopeMoved, was.version, now.version)
}

// release gives the scope back without changing anything, for a body that
// failed before it wrote.
func (r *Replacer) release(ctx context.Context) error {
	committed, err := r.s.GuardedBatch(ctx, nil, []Write{{
		Op: Update, Entity: r.sc.Entity, Key: r.sc.Key,
		Remove: []string{markerHolder, markerSince},
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
		Remove: []string{markerHolder, markerSince},
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

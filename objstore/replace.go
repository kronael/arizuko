package objstore

// replace.go is the versioned replace: make a scope's contents equal a
// document, and version the result (spec 6/40 §3). Its one consumer is
// resreg.Apply.
//
// It cannot be one GuardedBatch: the row set is unbounded and a replace issues
// a delete AND an insert per row against MaxBatchActions. So the shape is
// Query, a start-marker CAS, N guarded writes, a finish CAS, and the
// intermediate state is real. There is NO pre-image and NO rollback: a replace
// that dies half-way leaves the scope PART-WRITTEN with its marker held, and
// recovery is to run the same work again (roll-forward).
//
// The claim is NOT released when a body fails after writing: a released
// half-written scope reads as settled and correct, which is silent
// corruption; a held one answers ErrScopeHeld and names `since`. A second
// replace whose INTENT equals the held one's takes the claim over, which is
// safe because a replace makes a scope EQUAL a document. Liveness is NOT
// inferred from `since`: a slow replace on another host looks identical to a
// dead one, so a claim with a different intent is the operator's call
// (Claim.Take). A takeover skips the version check — no state the caller
// planned against still exists — and says so through Replacer.TookOver.

import (
	"context"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/core"
)

var (
	// ErrScopeMoved is the answer when the scope's version is not the one the
	// caller planned against. Apply's `force` is an empty wantVersion.
	ErrScopeMoved = errors.New("the scope moved since the caller read it")

	// ErrClaimLost is the answer when the claim this replace holds is no longer
	// on the marker. It is not transient: the scope is part-written by two
	// callers, so the remaining writes do not go in.
	ErrClaimLost = errors.New("the replace no longer holds the scope")
)

// Claim says what a replace is about to do, so a replace that dies part-way can
// be finished instead of wedging the scope for good.
type Claim struct {
	// Intent names the WORK: two replaces that would make the scope equal the
	// same document carry the same value, derived from that document by
	// resreg.applyClaim and nowhere else. It is REQUIRED: a claim with no
	// intent is a lock nothing can prove its own (BUGS L267).
	Intent string

	// Take adopts a claim whose intent DIFFERS: an operator gesture, never a
	// default. Rows the dead replace was part-way through OUTSIDE this
	// document's delete set stay part-written and the scope settles over them.
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
// so the scope was already part-written when this body started. A body reads
// it to drop the guards that assumed otherwise: resreg.Apply's checksum
// comparison would refuse the only action that can settle the scope.
func (r *Replacer) TookOver() bool { return r.tookOver }

// Replace runs body under an exclusive claim on the scope and versions the
// result. It answers the scope's new version. wantVersion is the version the
// caller planned against; empty skips the check. body reads the scope UNDER
// the claim, which keeps a second replace's rows out of this one's delete
// set; the claim gates replaces, not bounded writes.
//
// A HELD scope is refused unless cl adopts the work that holds it. A body that
// fails before writing releases the claim; one that fails after writing, or
// after taking over a part-written scope, keeps it.
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
	// A held scope has already moved past every version a caller could have
	// planned against.
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
// larger than one transaction holds. Every batch carries a Check on the marker
// as the fence, so it holds one write fewer than MaxBatchActions.
//
// One call names each item at most once: a delete and the insert over the
// same coordinate are TWO calls, because in one they would collide or not
// depending on where the batch boundary fell. A batch boundary is not atomic;
// every batch before a refusal has already committed. committed=false is the
// caller's own condition refusing; a lost fence is ErrClaimLost.
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
			// A refusal is one bit: re-read the marker to say which of the two
			// refused.
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

// claim takes the scope, refusing if anybody else took it first: an Update
// conditioned on the version this call read, or a create-if-absent when the
// marker is not there. The version is the WHOLE condition — every claim moves
// it in the write that sets the holder — and a takeover is the same write.
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
	// The refusal is one bit and the two causes need different answers.
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

// release gives the scope back without changing anything. It is the ONLY call
// that clears a claim without finishing the work, and Replace keeps it to a
// body that failed before writing on a settled scope.
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

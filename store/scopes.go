package store

// scopes.go is the version marker this package's manifest-visible entities move
// on every write, and the ONE site that moves it.
//
// WHY IT IS A MARKER AT ALL. resreg reads a subsystem's projection through
// objstore.ReadConsistent, which reads a scope's marker, runs its queries, and
// reads the marker again — there is no snapshot across N reads to open on a
// key-value engine (objstore/consistent.go). So it detects exactly the writers
// that put objstore.Bump into their own batch, and a writer that leaves it out
// is invisible to every export: the export then reports itself consistent over
// a state no writer ever saw. BUGS L264, live from the day onbod's three
// resources became entities and the read transaction that had EXCLUDED a
// concurrent write went with them.
//
// WHY THE DECLARATION IS HERE rather than beside its reader. resreg imports
// store, so a scope declared in resreg cannot be reached by the writers, which
// are in this package. It lives here for the reason resreg.SubsystemOnbod IS
// store.OwnerOnbod: one declaration, and the reader that needs it at another
// time or in another shape reads it.
//
// FORGETTING IS NOT EXPRESSIBLE. No writer in this package names a Bump. The
// helpers below derive it from the entities the batch touches, and
// scopes_test.go fails on any driver write that goes round them — the shape
// runAudited gives the audit row (audit_helpers.go), for the same reason: the
// failure is silence, and silence is not caught by review.
//
// NEITHER IS DROPPING THE ANSWER, for the same reason. GuardedBatch answers
// (committed, error), and committed=false with a nil error is the NORMAL outcome
// of a condition that did not hold — a claim someone else won, an Update on an
// item that is not there. It is not a fault and it never will be
// (objstore.Store). So `if _, err := …; err != nil` reports success for a write
// that never happened, and fifty-four call sites in this package wrote exactly
// that: `PUT /v1/gates/<gate>` carrying {"enabled":true} against a gate nobody
// opened answered 200 {"ok":true} over zero rows (BUGS L268).
//
// It is ONE type problem and not fifty-four review problems, so the type is what
// answers it. The three writers return an ERROR and no verdict, and a refusal is
// ErrRefused naming the batch, so `if _, err := …` no longer compiles: the site
// somebody writes next week is a build failure rather than a silent 200. A
// caller whose condition may legitimately refuse — a claim another caller wins, a
// create-if-absent whose refusal means the row is already right — calls the Try
// door beside it, where the NAME says the false was expected.
//
// FIFTEEN of the fifty-four were that, and they are the reason the Try door
// exists rather than an audit that fixed thirty-nine and left the rest looking
// identical to the bug. scopes_test.go holds the line: no door may drop its
// error, and a Try door's verdict may go to `_` only under a comment saying what
// a refusal MEANS there. The fourth shape needs no rule — a verdict bound to a
// name and never read is a compile error.
//
// incrementEntity keeps its verdict. It is unexported with three callers, two of
// which raise a refusal the way these writers now do and one of which — the
// invite decrement RestoreInvite compensates with — writes down why a refusal is
// the answer it wants.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// ScopeEntity holds one item per subsystem: the version marker a replace claims
// and a consistent read compares (objstore.Scope).
//
// It is an entity of its OWN rather than an item beside the rows it versions,
// because a marker sitting inside a resource's own partition would arrive in
// that resource's ScanAll as an item with no attributes it recognises.
const ScopeEntity = "resreg_scopes"

// scopePartition is the one partition every subsystem's marker lives in. There
// is one item per subsystem and two subsystems, so the whole-partition read this
// costs is a read of at most two items.
const scopePartition = "resreg"

// OwnerScope names the item carrying one owner's version marker.
//
// The owner is the unit because that is the unit resreg's two unbounded
// protocols operate on: `arizuko apply` replaces one subsystem's
// manifest-visible projection and `archive export` reads one subsystem's
// document, and a subsystem IS an owner database (resreg.SubsystemOnbod).
// A finer scope would let two applies to one owner database interleave, which
// is exactly what the column path's write lock prevents.
func OwnerScope(owner string) objstore.Scope {
	return objstore.Scope{
		Entity: ScopeEntity,
		Key:    objstore.Key{Partition: scopePartition, Sort: owner},
	}
}

// scopedEntities names each entity whose writes move a marker, and the owner
// whose scope they move.
//
// It is a declaration of its own rather than EntitiesByOwner read twice,
// because the marker versions a subsystem's MANIFEST-VISIBLE projection and not
// its database. PaneSessions and SecretUseLog are absent deliberately: no
// resreg resource reads them, so no export ever compares a marker over them,
// and a secret use — one per tool call that resolves a secret — would move
// routd's marker under an export that does not read it, spending one of
// ReadConsistent's eight attempts every time. audit_log is absent for the same
// reason and one more: four owner databases carry it and it is append-only.
//
// THE HOT TIER IS ABSENT FOR THAT REASON AND HARDER. Chats, Sessions,
// ChatReplyState, ChatProactive, SystemMessages and UserProfiles are runtime
// state rather than configuration, so no resreg resource projects them and no
// export reads them — and most are written on the HOT PATH: a cursor moves once
// per inbound message and an engagement window once per turn. Marking them would
// move routd's marker under every export that does not read them, which on a
// busy instance is an export that never completes.
//
// An entity-backed resreg resource missing from here is a resource an export
// cannot see the writes to. resreg/resources proves the set covers every one it
// registers, because that is where the registrations are known.
var scopedEntities = map[string]string{
	Onboarding:      OwnerOnbod,
	OnboardingGates: OwnerOnbod,
	Invites:         OwnerOnbod,

	// routd's thirteen. Every one of them is a registered resource with a
	// RowType, so `archive export` reads all thirteen — SkipApplyRebuild keeps a
	// resource out of the REBUILD, never out of the read.
	//
	// Counters is deliberately absent, and is the only routd entity this package
	// writes that is: it holds no row any export reads, and the write that draws
	// a number from it is always followed by the write that USES the number,
	// which moves the marker itself.
	ACL:               OwnerRoutd,
	ACLMembership:     OwnerRoutd,
	Groups:            OwnerRoutd,
	Routes:            OwnerRoutd,
	RouteTokens:       OwnerRoutd,
	ScheduledTasks:    OwnerRoutd,
	Secrets:           OwnerRoutd,
	PendingActions:    OwnerRoutd,
	NetworkRules:      OwnerRoutd,
	ProxydRoutes:      OwnerRoutd,
	WebRoutes:         OwnerRoutd,
	InstalledPackages: OwnerRoutd,
	GitHubSources:     OwnerRoutd,
}

// EntityScope answers the scope an entity's writes move, and whether it has one.
func EntityScope(entity string) (objstore.Scope, bool) {
	owner, ok := scopedEntities[entity]
	if !ok {
		return objstore.Scope{}, false
	}
	return OwnerScope(owner), true
}

// scopeBumps renders the marker writes a batch owes, one per distinct scope it
// touches, in owner order.
//
// The order is sorted rather than the order the writes arrived in, because a
// batch is compared and replayed and two runs of the same mutation must render
// the same actions. One per scope, because a batch may name each item once
// (objstore.ValidateBatch) and a second bump of one marker is that same item.
func scopeBumps(writes []objstore.Write) []objstore.Write {
	var owners []string
	for _, w := range writes {
		owner, ok := scopedEntities[w.Entity]
		if ok && !slices.Contains(owners, owner) {
			owners = append(owners, owner)
		}
	}
	slices.Sort(owners)
	bumps := make([]objstore.Write, 0, len(owners))
	for _, owner := range owners {
		bumps = append(bumps, objstore.Bump(OwnerScope(owner)))
	}
	return bumps
}

// withBumps is the caller's writes followed by the markers they move.
//
// It COPIES, because a caller chunking a larger slice hands over writes[:n] and
// appending in place would overwrite the first write of the next chunk with a
// marker — a mutation dropped, silently, on exactly the sweeps that carry the
// most of them (applyChunks).
func withBumps(writes []objstore.Write) []objstore.Write {
	return append(slices.Clone(writes), scopeBumps(writes)...)
}

// ErrRefused is what the three must-commit writers below answer when a batch's
// condition did not hold. It is the committed=false GuardedBatch reports, raised
// to an error HERE and nowhere below: a driver that reported it would make every
// claim unwrap a sentinel to learn that its own logic worked (objstore.Store).
//
// It is a sentinel because a caller one layer up reads it: onbod answers
// `PUT /v1/gates/<gate>` 404 when the gate's Update is refused, which is the
// answer the 200 in BUGS L268 was hiding.
var ErrRefused = errors.New("a condition did not hold, so the batch wrote nothing")

// WriteEntities is the GuardedBatch every writer of a scoped entity issues: the
// caller's writes plus the marker every scoped one of them moves, in the
// caller's own batch, so the mutation and the version go in together or neither
// does.
//
// It is EXPORTED because three of routd's thirteen entities — `pending_actions`,
// `web_routes` and `installed_packages` — are declared here and WRITTEN by routd
// (pending_actions.go, web_routes.go, installed_packages.go each say so). A
// writer that reached objstore directly would be invisible to
// objstore.ReadConsistent and therefore to every `arizuko export` of routd's
// projection (BUGS L264), so the marker is not the caller's to remember: it is
// derived here from the entities the batch touches.
//
// It returns an ERROR and no verdict, because a verdict is what fifteen callers
// dropped (the header above). A refusal is ErrRefused, naming the batch that
// wrote nothing. A caller whose condition may legitimately refuse calls
// TryWriteEntities.
func (s *Store) WriteEntities(ctx context.Context, checks []objstore.Check,
	writes ...objstore.Write) error {
	ok, err := s.TryWriteEntities(ctx, checks, writes...)
	return refusal(ok, err, checks, writes)
}

// TryWriteEntities is WriteEntities for the caller that ASKED a question: a
// claim that another caller may win, a create-if-absent whose refusal means the
// row is already right. It answers what GuardedBatch answers, and the name is
// what says the false is expected.
func (s *Store) TryWriteEntities(ctx context.Context, checks []objstore.Check,
	writes ...objstore.Write) (bool, error) {
	return s.Obj().GuardedBatch(ctx, checks, withBumps(writes))
}

// refusal turns a verdict into the error a must-commit caller gets, so the two
// doors above cannot hold different opinions of what a false means.
//
// The error names the batch rather than the write that refused, because the
// driver does not say which one did — committed is ONE bit, deliberately
// (objstore.Store). A batch whose own writes carry no condition can still be
// refused: every scoped batch carries the version marker's Bump, which is an
// Update, and an Update against a marker nobody created refuses the whole batch
// (objstore.EnsureScope). Naming the caller's writes is what tells those two
// apart in the log — a refusal on a list of unconditional writes is a missing
// marker, not a missing row.
func refusal(committed bool, err error, checks []objstore.Check, writes []objstore.Write) error {
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	named := make([]string, 0, len(checks)+len(writes))
	for _, c := range checks {
		named = append(named, "check "+c.Entity+"/"+c.Key.Partition+"/"+c.Key.Sort)
	}
	for _, w := range writes {
		named = append(named, w.Op.String()+" "+w.Entity+"/"+w.Key.Partition+"/"+w.Key.Sort)
	}
	return fmt.Errorf("%w: %s", ErrRefused, strings.Join(named, ", "))
}

// incrementEntity is objstore.Increment with the marker among its companion
// writes, so a counter that moves is as visible to a consistent read as a put
// is.
//
// The version is rendered once, when the call starts, and every retry re-sends
// that same companion — which is what the protocol wants: a refused attempt
// wrote nothing, and the attempt that commits leaves a value no other call
// produced, which is all a reader comparing two point reads asks for.
func (s *Store) incrementEntity(ctx context.Context, entity string, key objstore.Key,
	attr string, delta int, cond []objstore.Cmp, also []objstore.Write) (int, bool, error) {
	return s.incrementEntityVia(ctx, entity, key, attr, delta, cond, also)
}

// incrementEntityVia is incrementEntity on a transaction the caller owns when it
// owns one, and on the pool when it does not. It is the counter's twin of
// writeVia, and it exists for the same reason: a body that has to run both ways
// is written once.
func (s *Store) incrementEntityVia(ctx context.Context, entity string,
	key objstore.Key, attr string, delta int, cond []objstore.Cmp,
	also []objstore.Write) (int, bool, error) {
	counted := append([]objstore.Write{{Entity: entity, Key: key}}, also...)
	return objstore.Increment(ctx, s.Obj(), entity, key, attr, delta, cond,
		append(slices.Clone(also), scopeBumps(counted)...))
}

// EntityQuerier is one read's handle. objstore.Store satisfies it.
//
// It is exported because routd's cycle guard hands its own handle to
// MembershipWouldCycle.
type EntityQuerier interface {
	Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error)
}

// putIfAbsent is the funnel's create-if-absent, for an opener holding a Driver
// rather than a *Store: it seeds an item and leaves an existing one alone.
//
// It is HERE and not beside its caller for the reason the three writers above
// are: a call that reached the driver itself would be invisible to every
// consistent read over the scope it moves, and scopes_test.go is the lock on
// that door. A refusal is the item already being there, which is the outcome
// the caller wants — nothing else can refuse a create-if-absent.
func putIfAbsent(ctx context.Context, d objstore.Store, w objstore.Write) error {
	// A Merge already carries the semantics in its OP — it creates what it does
	// not find and leaves what it does — and refuses a condition outright, so it
	// is handed over as it came.
	if w.Op != objstore.Merge {
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
	}
	_, err := d.GuardedBatch(ctx, nil, withBumps([]objstore.Write{w}))
	return err
}

// ensureScopes creates the marker every scoped entity of one owner moves, and
// the entity it lives in.
//
// EnsureEntities calls it, so every opener that creates these entities creates
// the item their writes update. It has to exist BEFORE any write: a Bump is an
// Update, an Update against a missing item writes nothing, and the refusal
// takes the whole batch it rides in — the mutation lost with it
// (objstore.EnsureScope).
//
// An owner with no scoped entity gets nothing, which is what makes
// resreg.EnsureSubsystem's own answer for a column-shaped subsystem the same
// answer: the scope entity is not created where no marker is compared.
func ensureScopes(ctx context.Context, d Driver, owner string) error {
	if !slices.Contains(slices.Sorted(maps.Values(scopedEntities)), owner) {
		return nil
	}
	if err := d.CreateEntity(ctx, ScopeEntity); err != nil {
		return err
	}
	return objstore.EnsureScope(ctx, d, OwnerScope(owner))
}

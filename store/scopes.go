package store

// scopes.go is the ONE site that moves the version marker resreg's consistent
// reads compare (objstore.ReadConsistent). A writer that leaves the Bump out of
// its batch is invisible to every export, which then reports itself consistent
// over a state no writer saw (BUGS L264). The declaration is here rather than
// in resreg because resreg imports store.
//
// No writer names a Bump: the helpers derive it from the entities a batch
// touches, and scopes_test.go fails any driver write that goes round them.
//
// GuardedBatch's committed=false with a nil error is the NORMAL outcome of a
// condition that did not hold, and `if _, err := …` reported success for a
// write that never happened at fifty-four sites (BUGS L268). So WriteEntities
// returns an ERROR and no verdict, a refusal is ErrRefused, and a caller whose
// condition may legitimately refuse calls the Try door, where the name says
// the false is expected. scopes_test.go holds the line: no door drops its
// error, and a Try verdict goes to `_` only under a comment saying what a
// refusal means there.

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
// and a consistent read compares (objstore.Scope). An entity of its OWN,
// because a marker inside a resource's partition would arrive in that
// resource's ScanAll as an item it does not recognise.
const ScopeEntity = "resreg_scopes"

// scopePartition is the one partition every subsystem's marker lives in.
const scopePartition = "resreg"

// OwnerScope names the item carrying one owner's version marker. The owner is
// the unit because apply and export each operate on one subsystem, which IS an
// owner database; a finer scope would let two applies to one database
// interleave.
func OwnerScope(owner string) objstore.Scope {
	return objstore.Scope{
		Entity: ScopeEntity,
		Key:    objstore.Key{Partition: scopePartition, Sort: owner},
	}
}

// scopedEntities names each entity whose writes move a marker, and the owner
// whose scope they move.
//
// PaneSessions, SecretUseLog, audit_log and the hot tier are absent
// deliberately: no resreg resource reads them, and a hot-path write would move
// routd's marker under every export that does not read it, spending one of
// ReadConsistent's attempts each time. resreg/resources proves the set covers
// every entity-backed resource it registers.
var scopedEntities = map[string]string{
	Onboarding:      OwnerOnbod,
	OnboardingGates: OwnerOnbod,
	Invites:         OwnerOnbod,

	// routd's thirteen. Counters is absent: the write that USES a drawn number
	// moves the marker itself.
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
// touches. Sorted, so two runs of one mutation render the same actions; one
// per scope, because a batch may name each item once (objstore.ValidateBatch).
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

// withBumps is the caller's writes followed by the markers they move. It
// COPIES: a caller chunking a larger slice hands over writes[:n], and appending
// in place would overwrite the next chunk's first write with a marker.
func withBumps(writes []objstore.Write) []objstore.Write {
	return append(slices.Clone(writes), scopeBumps(writes)...)
}

// ErrRefused is what the must-commit writers answer when a batch's condition
// did not hold. A sentinel because a caller one layer up reads it: onbod
// answers 404 on a refused gate update (BUGS L268).
var ErrRefused = errors.New("a condition did not hold, so the batch wrote nothing")

// WriteEntities is the GuardedBatch every writer of a scoped entity issues: the
// caller's writes plus the marker each scoped one moves, in one batch.
//
// EXPORTED because routd writes three of the entities declared here
// (pending_actions, web_routes, installed_packages); a writer that reached
// objstore directly would be invisible to every export (BUGS L264). It returns
// an ERROR and no verdict (file header); a refusal is ErrRefused.
func (s *Store) WriteEntities(ctx context.Context, checks []objstore.Check,
	writes ...objstore.Write) error {
	ok, err := s.TryWriteEntities(ctx, checks, writes...)
	return refusal(ok, err, checks, writes)
}

// TryWriteEntities is WriteEntities for the caller that ASKED a question: the
// name says the false is expected.
func (s *Store) TryWriteEntities(ctx context.Context, checks []objstore.Check,
	writes ...objstore.Write) (bool, error) {
	return s.Obj().GuardedBatch(ctx, checks, withBumps(writes))
}

// refusal turns a verdict into the error a must-commit caller gets. It names
// the batch, not the write: committed is ONE bit. A batch of unconditional
// writes can still refuse, because the Bump is an Update against a marker
// nobody created (objstore.EnsureScope); naming the writes tells that apart
// from a missing row in the log.
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
// writes. The version is rendered once and every retry re-sends it: a refused
// attempt wrote nothing.
func (s *Store) incrementEntity(ctx context.Context, entity string, key objstore.Key,
	attr string, delta int, cond []objstore.Cmp, also []objstore.Write) (int, bool, error) {
	return s.incrementEntityVia(ctx, entity, key, attr, delta, cond, also)
}

// incrementEntityVia renders the counted set and its bumps for
// objstore.Increment.
func (s *Store) incrementEntityVia(ctx context.Context, entity string,
	key objstore.Key, attr string, delta int, cond []objstore.Cmp,
	also []objstore.Write) (int, bool, error) {
	counted := append([]objstore.Write{{Entity: entity, Key: key}}, also...)
	return objstore.Increment(ctx, s.Obj(), entity, key, attr, delta, cond,
		append(slices.Clone(also), scopeBumps(counted)...))
}

// EntityQuerier is one read's handle. Exported because routd's cycle guard
// hands its own handle to MembershipWouldCycle.
type EntityQuerier interface {
	Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error)
}

// putIfAbsent is the create-if-absent for an opener holding a Driver. It is
// HERE so the write is visible to every consistent read over the scope it
// moves; a refusal is the item already being there.
func putIfAbsent(ctx context.Context, d objstore.Store, w objstore.Write) error {
	// A Merge already creates what it does not find and refuses a condition.
	if w.Op != objstore.Merge {
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
	}
	_, err := d.GuardedBatch(ctx, nil, withBumps([]objstore.Write{w}))
	return err
}

// ensureScopes creates the marker every scoped entity of one owner moves, and
// the entity it lives in. It has to exist BEFORE any write: a Bump is an
// Update, and an Update against a missing item refuses the whole batch
// (objstore.EnsureScope). An owner with no scoped entity gets nothing.
func ensureScopes(ctx context.Context, d Driver, owner string) error {
	if !slices.Contains(slices.Sorted(maps.Values(scopedEntities)), owner) {
		return nil
	}
	if err := d.CreateEntity(ctx, ScopeEntity); err != nil {
		return err
	}
	return objstore.EnsureScope(ctx, d, OwnerScope(owner))
}

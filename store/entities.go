package store

// entities.go names the tables this package has moved onto objstore, and
// prepares a database to serve them (spec 6/37 step 7).
//
// It is the declaration every reader shares: `migrate` applies it to the
// database it opens, and `arizuko migrate-store` applies it to the database it
// moves. One declaration, two readers — a loop copied into each would be two
// places for the set to drift, which is what BUGS F29 cost when a database
// booted healthy and dropped every audit row.
//
// The set is SMALL because the move is partial. `store.Store` still holds a
// *sql.DB for the tables that have not moved and reaches the entities below
// through the same pool's driver (Store.Obj), so each table has exactly one
// implementation at every moment: a moved one reads through objstore, an
// unmoved one through SQL. There is no fallback and no dual read.

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The entities this package reads. An entity is a name a driver resolves; what
// it costs to reach an item inside one is the (partition, sort) coordinate each
// file documents.
const (
	// PaneSessions holds one item per Slack assistant pane, addressed by the
	// (team, user, thread) triple that opened it.
	PaneSessions = "pane_sessions"
	// SecretUseLog holds one item per resolved secret a tool call read,
	// addressed by the instant it happened.
	SecretUseLog = "secret_use_log"
	// Onboarding holds one item per unrouted chat awaiting admission,
	// addressed by its jid.
	Onboarding = "onboarding"
	// OnboardingGates holds one item per admission gate, addressed by the
	// gate key.
	OnboardingGates = "onboarding_gates"
	// Invites holds one item per live invite, addressed by its ref.
	Invites = "invites"
)

// EntitiesByOwner is the set a driver must be able to serve before this package
// answers, KEYED BY THE DATABASE THAT HOLDS IT.
//
// It is keyed by owner rather than being one flat list because an entity lives
// in exactly one database and a caller that asked for the whole set would grow
// tables its own readers know nothing about — routd.db carrying onbod's three,
// so that the next person reading `.tables` on it has to work out which daemon
// owns which. One subsystem is one owner database (spec 5/8), and nothing would
// have failed: the drift is silent by construction.
//
// It is still ONE declaration, because store is what reads and writes all five;
// what the owner key decides is the SET a caller asks for, not who declares it.
//
// audit_log is NOT here: four owner databases carry it, `audit` owns the
// declaration, and every opener reaches it through audit.EnsureEntities.
//
// `invite_redemptions` is NOT here either, and that is a modelling answer
// rather than an unfinished move. Its identity is an AUTOINCREMENT rowid the
// engine hands out, and the two operations on it are "the caller's most recent
// unspent row" and "delete exactly that row" — a claim that has to pick one of
// several rows one caller owns (onbod/main.go latestRedemption, spendRedemption).
// An item has no server-minted identity, so a coordinate for it would have to
// be invented, and inventing one is a decision the row that needs it should
// make. It stays a table in onbod.db and store.consumeInvite keeps writing it
// as SQL, inside the same transaction the invite's counter moves in.
var EntitiesByOwner = map[string][]string{
	OwnerRoutd: {PaneSessions, SecretUseLog},
	OwnerOnbod: {Onboarding, OnboardingGates, Invites},
}

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares ONE owner's database to hold the entities this
// package reads there. Four openers reach it — routd's, onbod's, store's own
// migrate and the migration tools — and a loop copied into each is four places
// for the set to drift. Asking twice is a no-op.
//
// The owner is a parameter rather than something derived from the handle,
// because a *sql.DB does not know which file it opened and guessing from a path
// is the derivation this repo has an outage on record for (CLAUDE.md). An owner
// with no entities declared is a caller mistake and fails loudly: a silent
// no-op there is a database that boots healthy and drops every write.
//
// audit's entity is NOT created here even though every database that holds
// these also holds one. audit owns that declaration, four owners share it, and
// `arizuko migrate-audit` moves all four in one run — so a caller that needs
// both calls both, the way runed/db.go does. Bundling them made this package's
// migration tool refuse to run until audit's had, on a database where the two
// moves are independent.
func EnsureEntities(ctx context.Context, m EntityMaker, owner string) error {
	entities, ok := EntitiesByOwner[owner]
	if !ok {
		return fmt.Errorf("store: %q owns none of this package's entities", owner)
	}
	for _, entity := range entities {
		if err := m.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return nil
}

// tsWidth is core.TS's width. A test checks it against core.TS itself, because
// a constant that drifted from the layout would refuse every write instead of
// guarding one.
const tsWidth = 30

// setAttr records one optional attribute, leaving it off the item when the
// caller does not carry it. Absent is the only null this model has, so an empty
// string must never be written where a reader tests for the field's presence.
func setAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// applyInChunks sends writes a batch at a time and answers how many landed.
//
// MaxBatchActions is the weakest engine's transaction bound, so a sweep that
// touches more items than one transaction may carry respects it too. Each chunk
// commits on its own: the writes are independent, so a chunk that lands and a
// chunk that does not leave work a re-run finishes rather than a half-applied
// transaction.
func (s *Store) applyInChunks(ctx context.Context, what string,
	writes []objstore.Write) (int, error) {
	return applyChunks(ctx, what, writes, func(ctx context.Context, chunk []objstore.Write) (bool, error) {
		return s.Obj().GuardedBatch(ctx, nil, chunk)
	})
}

// applyChunks is the body of the sweep, taking the commit as a parameter so a
// caller that owns a transaction runs the identical chunking rather than a
// second loop beside it (CarryOnboardingLegacy, BackfillInviteRefs).
func applyChunks(ctx context.Context, what string, writes []objstore.Write,
	commit func(context.Context, []objstore.Write) (bool, error)) (int, error) {
	done := 0
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := commit(ctx, writes[:n])
		if err != nil {
			return done, fmt.Errorf("%s: %w", what, err)
		}
		if !committed {
			return done, fmt.Errorf("%s: a batch of %d was refused, so an item it "+
				"conditions on changed under it", what, n)
		}
		done += n
		writes = writes[n:]
	}
	return done, nil
}

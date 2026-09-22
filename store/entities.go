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
// Every table this package serves reads through objstore (`Store.Obj`). There
// is one implementation per table, no fallback and no dual read.

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
	// InviteRedemptions holds one unspent subgroup-invite redemption, addressed
	// by the person who followed it and the instant they did.
	InviteRedemptions = "invite_redemptions"

	// routd's thirteen registered resources. Each one's coordinate is argued
	// against the live rows in resreg/resources/<name>.go, because the
	// coordinate is what an apply addresses a row BY and a wrong one collides
	// two rows onto one item (BUGS L260). The separator six of them join on is
	// KeySep below.

	// ACL holds one grant, addressed by the six fields that ARE the grant:
	// principal, action, scope, params, predicate and effect.
	ACL = "acl"
	// ACLMembership holds one membership edge, addressed by (child, parent).
	ACLMembership = "acl_membership"
	// Groups holds one group, addressed by its folder.
	Groups = "groups"
	// Routes holds one routing rule, addressed by (seq, target, match). `seq`
	// is padded so the read comes back in precedence order.
	Routes = "routes"
	// RouteTokens holds one delivery or pairing credential, addressed by
	// hex(sha256(token)) — the digest is the key, and the jid is not unique.
	RouteTokens = "route_tokens"
	// ScheduledTasks holds one scheduled prompt, addressed by its id.
	ScheduledTasks = "scheduled_tasks"
	// Secrets holds one sealed value, addressed by (scope_kind, scope_id, key).
	Secrets = "secrets"
	// PendingActions holds one held tool call, addressed by its id.
	PendingActions = "pending_actions"
	// NetworkRules holds one egress allowlist rule, addressed by
	// (folder, target). The folder leads because it is also the purge scope.
	NetworkRules = "network_rules"
	// ProxydRoutes holds one reverse-proxy route, addressed by its path.
	ProxydRoutes = "proxyd_routes"
	// WebRoutes holds one web access rule, addressed by its path prefix.
	WebRoutes = "web_routes"
	// InstalledPackages holds one install record, addressed by (folder, name).
	InstalledPackages = "installed_packages"
	// GitHubSources holds one webhook source, addressed by its source key.
	GitHubSources = "github_sources"

	// The hot tier's five, which no resreg resource projects: they are runtime
	// state rather than configuration, so nothing exports them and no version
	// marker moves with them (scopes.go).

	// Chats holds one item per chat routd has seen, addressed by its jid, and
	// carries `agent_cursor` — the dispatch watermark that decides whether a
	// message has already been fed to an agent.
	Chats = "chats"
	// Sessions holds one topic's continuity, addressed by (group_folder, topic),
	// and carries `observed_cursor` — the other watermark, over the ambient
	// window a prompt is built from.
	Sessions = "sessions"
	// ChatReplyState holds whether a reply is still owed on (jid, topic) and
	// which folder holds the conversation.
	ChatReplyState = "chat_reply_state"
	// ChatProactive holds one chat's unprompted-turn cooldown, addressed by its
	// jid.
	ChatProactive = "chat_proactive"
	// SystemMessages holds one queued system event, addressed by the folder it
	// is queued for and the instant it was queued.
	SystemMessages = "system_messages"
	// UserProfiles holds one item per person the instance knows a name for,
	// addressed by their sub, and carries the username their folder is named
	// after and their per-day spend cap.
	UserProfiles = "user_profiles"
	// TurnResults holds one item per (folder, turn) the agent has reported an
	// outcome for. Its create-if-absent is the claim that makes one report one
	// delivery.
	TurnResults = "turn_results"
	// GroupWatchers holds one item per (observer, source) subscription and no
	// attributes at all: the pair IS the fact.
	GroupWatchers = "group_watchers"
	// TaskRunLogs holds one item per execution of a scheduled task, addressed by
	// the instant it ran — which IS its identity, so no separate `run_at`.
	TaskRunLogs = "task_run_logs"
	// TurnContexts holds one item per turn routd has dispatched, addressed by
	// its turn id — the only handle a late callback carries. The name is plural
	// where the table it replaces is singular, because the singular one is the
	// struct a caller reads back.
	TurnContexts = "turn_context"

	// Messages holds one item per message routd has appended, addressed by the
	// chat it is in and by `core.TS(timestamp) + KeySep + id` inside it. The id
	// is in the key because 245 pairs share one instant inside one chat on
	// krons, so a time-only key would put two messages on one item.
	Messages = "messages"
	// MessageFolders is the folder feed: one pointer per message that carries a
	// `routed_to`, addressed by that folder and by the message's own position.
	MessageFolders = "message_folders"
	// MessageFolderCounts holds one folder's message count, addressed by the
	// folder, so the usage page reads 95 items instead of 95 partitions.
	MessageFolderCounts = "message_folder_counts"
	// MessageActivity is the cross-chat feed: one projection per message in one
	// constant partition, swept at ActivityRetention.
	MessageActivity = "message_activity"
	// MessageDelivery holds the delivery state of a row that is NOT sent,
	// addressed by that status and by the message's own position. `sent` is the
	// absence of an item.
	MessageDelivery = "message_delivery"

	// Counters holds one item per server-minted number, addressed by the name
	// of what it numbers, with the count in `n`.
	//
	// There is exactly one today and it is routes.id: AUTOINCREMENT is a SQL
	// facility, an item has no server-minted identity, and two concurrent
	// add_route calls reading `max(id) + 1` would mint the same handle for two
	// rules — after which delete_route(id) names both. objstore.Increment over
	// this item is the compare-and-swap that makes the mint one winner
	// (protocol.go). The apply path mints differently and correctly, because it
	// sees the whole row set at once (resreg/entity.go mintNumbers).
	Counters = "counters"
)

// KeySep joins the fields of a composite coordinate, and it is declared HERE
// because both readers of it are downstream of this package: the writers in
// this file's neighbours, and the resource declarations in resreg/resources,
// which reach it the way they already reach OwnerRoutd. Two spellings would put
// one writer's items at a coordinate the other's read never names.
//
// Six of routd's thirteen resources join on it: acl is the six-field grant,
// acl_membership is (child, parent), network_rules is (folder, target),
// installed_packages is (folder, name), secrets is (scope_kind, scope_id, key)
// and routes is (seq, target, match). No value of any of those fields may carry
// it, and a write that does is REFUSED rather than splitting the key somewhere
// the read would not put it back together.
//
// WHY THIS BYTE. The obvious candidates are all in the data: "/" separates a
// folder from its children and a topic from its folder, ":" a principal's kind
// from its name, "=" and "," the pairs inside an acl row's `params`, "#" a
// topic inside a route target. Measured over every one of those fields on all
// three live instances 2026-09-08, the whole character set is space,
// `#*,-./0-9:=@_`, the letters, and one emoji. ASCII 0x1F is the unit
// separator — the byte whose only job is this — and it is what
// store/pane_sessions and the engine's own composite fixtures already use.
//
// Measuring today's rows is not the guarantee and is not meant to be. The
// guarantee is the refusal.
const KeySep = "\x1f"

// The counters this package mints, and the attribute each is held in.
const (
	// CounterRouteID is the largest route id handed out.
	CounterRouteID = "routes.id"
	// counterAttr holds the count. One name, because one protocol reads it.
	counterAttr = "n"
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
// `invite_redemptions` carries a coordinate this package INVENTED, because an
// item has no server-minted identity and its two operations needed one: "the
// caller's most recent unspent redemption" and "spend exactly that one"
// (onbod/main.go). The person is the partition and the instant is the sort key,
// so the first is a descending range of one item and the second a point delete
// whose refusal IS the claim. Its write joins the transaction the invite's
// counter moves in, through WriteEntitiesInTx.
var EntitiesByOwner = map[string][]string{
	OwnerRoutd: {
		PaneSessions, SecretUseLog, Counters,
		ACL, ACLMembership, Groups, Routes, RouteTokens, ScheduledTasks, Secrets,
		PendingActions, NetworkRules, ProxydRoutes, WebRoutes, InstalledPackages,
		GitHubSources,
		Chats, Sessions, ChatReplyState, ChatProactive, SystemMessages, UserProfiles,
		TurnResults, GroupWatchers, TaskRunLogs, TurnContexts,
		Messages, MessageFolders, MessageFolderCounts, MessageActivity, MessageDelivery,
	},
	OwnerOnbod: {Onboarding, OnboardingGates, Invites, InviteRedemptions},
}

// Driver is what preparing this package's entities takes: an engine that
// declares them, and the store that writes through them.
//
// CreateEntity is not on objstore.Store because schema is driver-local and an
// engine with no DDL implements it as a no-op (spec 6/40). It is joined to the
// store here because one of the things prepared is not a table but an ITEM —
// the version marker every scoped write moves, which has to be there before the
// first of them (scopes.go).
type Driver interface {
	objstore.Store
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares ONE owner's database to hold the entities this
// package reads there. Four openers reach it — routd's, onbod's, store's own
// migrate and the migration tools — and a loop copied into each is four places
// for the set to drift. Asking twice is a no-op.
//
// The owner is a parameter rather than something derived from the handle: a
// handle does not know whose partition it addresses, and guessing identity from
// a path is the derivation this repo has an outage on record for (CLAUDE.md).
// An owner with no entities declared is a caller mistake and fails loudly — a
// silent no-op there is a store that boots healthy and drops every write.
//
// audit's entity is NOT created here even though every database that holds
// these also holds one. audit owns that declaration, four owners share it, and
// `arizuko migrate-audit` moves all four in one run — so a caller that needs
// both calls both, the way runed/db.go does. Bundling them made this package's
// migration tool refuse to run until audit's had, on a database where the two
// moves are independent.
func EnsureEntities(ctx context.Context, d Driver, owner string) error {
	entities, ok := EntitiesByOwner[owner]
	if !ok {
		return fmt.Errorf("store: %q owns none of this package's entities", owner)
	}
	return EnsureEntitySet(ctx, d, owner, entities...)
}

// EnsureEntitySet prepares exactly the entities NAMED, plus the owner's counters
// and version marker. It is the migration tools' door, and it exists because
// CreateEntity PROBES the three columns it needs: a tool that asked for the
// whole owner's set would fail on a table a DIFFERENT tool has not moved yet,
// and both tools would refuse each other forever.
//
// `arizuko migrate-store` names the two tables it moves and `migrate-routd` the
// thirteen it moves, so either may run first. An opener still asks for the whole
// set through EnsureEntities, which is where the set is declared — this takes a
// list because a migration is the one caller that knows a subset is all it may
// touch.
func EnsureEntitySet(ctx context.Context, d Driver, owner string, entities ...string) error {
	for _, entity := range entities {
		if err := d.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	if err := ensureCounters(ctx, d, owner); err != nil {
		return err
	}
	return ensureScopes(ctx, d, owner)
}

// countersByOwner names the counters each owner mints. A counter has to EXIST
// before the first increment: Increment reads the item and refuses one that is
// not there, because "a counter with no zero is a modelling gap rather than a
// value to invent" (objstore.Increment).
var countersByOwner = map[string][]string{OwnerRoutd: {CounterRouteID}}

// ensureCounters seeds each of one owner's counters at zero, once.
//
// It leaves an existing counter alone — the Put is conditioned on the item
// being absent — so an opener calls it on every boot and a migration that set
// the counter to the largest id it carried over keeps that value.
func ensureCounters(ctx context.Context, d Driver, owner string) error {
	names := countersByOwner[owner]
	if len(names) == 0 {
		return nil
	}
	// The entity its own seeding needs, created here for the reason ensureScopes
	// creates the marker's: a caller naming a SUBSET of the owner's entities — a
	// migration tool — has no reason to name this one, and the seed below writes
	// into it.
	if err := d.CreateEntity(ctx, Counters); err != nil {
		return err
	}
	for _, name := range names {
		err := putIfAbsent(ctx, d, objstore.Write{
			Op:     objstore.Put,
			Entity: Counters,
			Key:    objstore.Key{Partition: owner, Sort: name},
			Attrs:  map[string]string{counterAttr: "0"},
		})
		if err != nil {
			return fmt.Errorf("seed counter %s/%s: %w", owner, name, err)
		}
	}
	return nil
}

// nextNumber hands out the next value of one counter, or fails.
//
// It is the object path's AUTOINCREMENT for a single imperative write, where
// resreg/entity.go's mintNumbers is the apply's: that one sees the whole row
// set and can take one past its largest, and this one has a set of one, so the
// serialisation has to come from the counter itself.
func (s *Store) nextNumber(ctx context.Context, owner, name string) (int, error) {
	n, committed, err := s.incrementEntity(ctx, Counters,
		objstore.Key{Partition: owner, Sort: name}, counterAttr, 1, nil, nil)
	if err != nil {
		return 0, err
	}
	if !committed {
		return 0, fmt.Errorf("counter %s/%s refused its own increment", owner, name)
	}
	return n, nil
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
		return s.TryWriteEntities(ctx, nil, chunk...)
	})
}

// applyChunks is the body of the sweep, taking the commit as a parameter so a
// caller that owns a transaction runs the identical chunking rather than a
// second loop beside it.
//
// A chunk is one action short of the bound, because every commit here carries
// the version marker its writes move as one more action (scopes.go) and a chunk
// filled to MaxBatchActions would be refused with it.
func applyChunks(ctx context.Context, what string, writes []objstore.Write,
	commit func(context.Context, []objstore.Write) (bool, error)) (int, error) {
	done := 0
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions-1)
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

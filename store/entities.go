package store

// entities.go declares the entities this package serves and prepares a
// database to hold them (spec 6/37 step 7). One declaration for every opener:
// a copied loop drifted once and a database booted healthy while dropping
// every audit row (BUGS F29). One read path per table, no fallback.

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The entities this package reads. The (partition, sort) coordinate of each is
// documented in its own file.
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

	// routd's thirteen registered resources. A wrong coordinate collides two
	// rows onto one item (BUGS L260); six of them join on KeySep.

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
	// hex(sha256(token)); the jid is not unique.
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

	// The hot tier: runtime state no resreg resource projects, so nothing
	// exports it and no version marker moves with it (scopes.go).

	// Chats holds one item per chat routd has seen, addressed by its jid, and
	// carries `agent_cursor`, the dispatch watermark.
	Chats = "chats"
	// Sessions holds one topic's continuity, addressed by (group_folder, topic),
	// and carries `observed_cursor`, the ambient-window watermark.
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
	// addressed by their sub.
	UserProfiles = "user_profiles"
	// TurnResults holds one item per (folder, turn) the agent has reported an
	// outcome for. Its create-if-absent is the claim that makes one report one
	// delivery.
	TurnResults = "turn_results"
	// GroupWatchers holds one item per (observer, source) subscription and no
	// attributes at all: the pair IS the fact.
	GroupWatchers = "group_watchers"
	// TaskRunLogs holds one item per execution of a scheduled task, addressed by
	// the instant it ran, which IS its identity.
	TaskRunLogs = "task_run_logs"
	// TurnContexts holds one item per turn routd has dispatched, addressed by
	// its turn id, the only handle a late callback carries.
	TurnContexts = "turn_context"

	// Messages holds one item per message routd has appended, addressed by the
	// chat and by `core.TS(timestamp) + KeySep + id` inside it. The id is in the
	// key because 245 pairs share one instant inside one chat on krons.
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
	// of what it numbers, with the count in `n`. An item has no server-minted
	// identity, so objstore.Increment over it is the compare-and-swap that
	// makes a mint one winner; the apply path mints from the whole row set
	// instead (resreg/entity.go mintNumbers).
	Counters = "counters"
)

// KeySep joins the fields of a composite coordinate. It is declared here
// because resreg/resources joins on it too; two spellings would put one
// writer's items where the other's read never looks. No joined field may carry
// it: a write that does is REFUSED. 0x1F is the ASCII unit separator, the one
// byte absent from every live value measured 2026-09-08; the refusal, not the
// measurement, is the guarantee.
const KeySep = "\x1f"

// The counters this package mints, and the attribute each is held in.
const (
	// CounterRouteID is the largest route id handed out.
	CounterRouteID = "routes.id"
	// counterAttr holds the count.
	counterAttr = "n"
)

// EntitiesByOwner is the set a driver must be able to serve before this package
// answers, KEYED BY THE DATABASE THAT HOLDS IT: an entity lives in exactly one
// database, and a flat list would grow routd's tables with onbod's three.
//
// audit_log is NOT here: four owner databases carry it, `audit` owns the
// declaration, and every opener reaches it through audit.EnsureEntities.
//
// `invite_redemptions` carries a coordinate this package invented: the person
// is the partition and the instant the sort key, so "the caller's most recent
// unspent redemption" is a descending range of one and "spend exactly that one"
// a point delete whose refusal IS the claim (onbod/main.go).
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

// Driver is what preparing this package's entities takes. CreateEntity is not
// on objstore.Store because schema is driver-local and an engine with no DDL
// implements it as a no-op (spec 6/40).
type Driver interface {
	objstore.Store
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntitySet prepares exactly the entities NAMED, plus the owner's counters
// and version marker. It is the migration tools' door: CreateEntity PROBES the
// columns it needs, so a tool that asked for the whole owner's set would fail
// on a table a DIFFERENT tool has not moved yet.
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
// before the first increment: objstore.Increment refuses a missing item.
var countersByOwner = map[string][]string{OwnerRoutd: {CounterRouteID}}

// ensureCounters seeds each of one owner's counters at zero, once. An existing
// counter is left alone, so a value a migration carried over survives.
func ensureCounters(ctx context.Context, d Driver, owner string) error {
	names := countersByOwner[owner]
	if len(names) == 0 {
		return nil
	}
	// A caller naming a SUBSET of the owner's entities has no reason to name
	// this one, and the seed below writes into it.
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

// nextNumber hands out the next value of one counter, or fails. It is the
// AUTOINCREMENT of a single imperative write; the apply path mints from the
// whole row set instead (resreg/entity.go mintNumbers).
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

// tsWidth is core.TS's width. A test checks it against core.TS itself.
const tsWidth = 30

// setAttr records one optional attribute, leaving it off the item when the
// caller does not carry it. Absent is the only null this model has, so an empty
// string must never be written where a reader tests for presence.
func setAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// applyInChunks sends writes a batch at a time and answers how many landed.
// Each chunk commits on its own: the writes are independent, so a partial run
// leaves work a re-run finishes.
func (s *Store) applyInChunks(ctx context.Context, what string,
	writes []objstore.Write) (int, error) {
	return applyChunks(ctx, what, writes, func(ctx context.Context, chunk []objstore.Write) (bool, error) {
		return s.TryWriteEntities(ctx, nil, chunk...)
	})
}

// applyChunks is the body of the sweep, taking the commit as a parameter. A
// chunk is one action short of MaxBatchActions because the version marker's
// Bump rides every commit (scopes.go).
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

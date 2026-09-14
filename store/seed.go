package store

// seed.go is the floor a routd.db is BORN with: the root grant the operator
// delegates from, the member role every folder is bound to, and the two egress
// rules without which no spawned agent reaches a model.
//
// WHY IT IS CODE AND NOT A MIGRATION. A seed is a row, and a row an entity
// holds goes in through that entity's own writer — the driver makes the entity
// after the chain has run, so a statement in the chain has no table to put one
// in. That is the whole reason this file exists rather than the INSERTs that
// used to sit in routd/migrations 0005, 0022 and 0023.
//
// WHY "WHEN THE ENTITY IS EMPTY" AND NOT "IF ABSENT ON EVERY OPEN". Once-ness
// is what the chain gave, and it is load-bearing: an operator whose manifest
// deliberately leaves a floor grant out must not have it written back under
// them at the next restart, or `arizuko plan` shows the same diff forever and
// the seeder and the manifest fight. An EMPTY entity is the one state that
// cannot be an operator's intent — a routd.db with no acl row at all grants
// nothing to anyone, including the operator — so it is the condition, and it is
// exactly "a database nobody has written yet".
//
// The write is still create-if-absent under that condition, because two openers
// can race the same fresh database (db_utils.Migrate serialises the chain, not
// this).
//
// THE INSTANTS ARE FIXED, not now(). A fresh instance's config checksum has to
// be the same value on every machine, or `arizuko export | apply` on a
// just-created instance reports a change nobody made.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// RoleOperator is the delegation root (spec 4/R, 5/33): it holds (*, **) WITH
// GRANT OPTION, so it may delegate anything. Every other grant starts without
// the option and gains it only when explicitly granted.
const RoleOperator = "role:operator"

// memberVerbs are the twelve messaging verbs role:member confers — the floor,
// at scope **, because containment is authorizeJID route-ownership at runtime
// and not the acl scope. The floor is NOT re-delegable: every child is born a
// member directly, so these carry no grant option.
var memberVerbs = []string{
	"mcp:reply", "mcp:send", "mcp:send_file", "mcp:send_voice",
	"mcp:post", "mcp:forward", "mcp:quote", "mcp:repost",
	"mcp:like", "mcp:dislike", "mcp:edit", "mcp:delete",
}

// alwaysMountedTools are the READS every turn mounts, plus the memory
// checkpoint — the class spec 5/33 exempts from grants entirely. They get no
// ALLOW row because they need none; what they need is the skip row below, or a
// folder holding a blanket hold would wait for a human before it could read its
// own notes or the message it is answering.
//
// The list is the READS and only the reads. A write that happens to mount
// unconditionally — invite_create, pin_message, reset_session — stays held: the
// whole point of the blanket hold is that a person sees those.
// routd's TestWorld_HoldsOnlyWhatAPersonShouldSee walks the mounted set and
// fails if a new tool lands on the wrong side of this line.
var alwaysMountedTools = []string{
	"mcp:inspect_routing", "mcp:inspect_tasks", "mcp:inspect_session",
	"mcp:inspect_identity", "mcp:inspect_messages", "mcp:find_messages",
	"mcp:get_thread", "mcp:fetch_history", "mcp:get_work", "mcp:set_work",
}

// skipApprovalFloor names every tool role:member answers WITHOUT a human. It is
// the other half of the inversion a world folder is granted (onbod writeWorld):
// the world holds `hold:mcp:*`, so EVERY call it makes waits for its owner
// unless a narrower rule says otherwise, and these are that rule.
//
// Deny on a `hold:` action means SKIP APPROVAL — it denies the hold, never the
// tool (spec 6/26 § Decision 5). Deny-wins is absolute, so this floor beats the
// world's wildcard however the world was granted.
func skipApprovalFloor() []string {
	out := make([]string, 0, len(memberVerbs)+len(alwaysMountedTools))
	out = append(out, memberVerbs...)
	return append(out, alwaysMountedTools...)
}

// baseAllowlist is the instance-wide egress every folder inherits
// (folderAncestry). Without it a spawned container reaches no model at all.
var baseAllowlist = []string{"anthropic.com", "api.anthropic.com"}

// The instants the floor carries. Fixed, so two fresh instances agree.
var (
	operatorGrantedAt  = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	memberGrantedAt    = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	allowlistAddedAt   = time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	skipFloorGrantedAt = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
)

// SeedRoutdFloor writes the floor into a routd.db that holds none of it.
//
// It runs where EnsureEntities does, so every opener of routd.db reaches it:
// `arizuko create`, the split cutover, and every test database. An instance
// that already carries grants or egress rules is left exactly as it is.
func SeedRoutdFloor(ctx context.Context, d objstore.Store) error {
	empty, err := entityIsEmpty(ctx, d, ACL)
	if err != nil {
		return err
	}
	if empty {
		rows := []core.ACLRow{{
			Principal: RoleOperator, Action: "*", Scope: "**", Effect: "allow",
			GrantedBy: "system", GrantedAt: core.TS(operatorGrantedAt), GrantOption: true,
		}}
		for _, verb := range memberVerbs {
			rows = append(rows, core.ACLRow{
				Principal: RoleMember, Action: verb, Scope: "**", Effect: EffectAllow,
				GrantedBy: "system:4r", GrantedAt: core.TS(memberGrantedAt),
			})
		}
		for _, verb := range skipApprovalFloor() {
			rows = append(rows, core.ACLRow{
				Principal: RoleMember, Action: HoldPrefix + strings.TrimPrefix(verb, "mcp:"),
				Scope: "**", Effect: EffectDeny,
				GrantedBy: "system:5r", GrantedAt: core.TS(skipFloorGrantedAt),
			})
		}
		for _, r := range rows {
			w, err := aclPut(r)
			if err != nil {
				return fmt.Errorf("seed the acl floor: %w", err)
			}
			if err := putIfAbsent(ctx, d, w); err != nil {
				return fmt.Errorf("seed %s %s: %w", r.Principal, r.Action, err)
			}
		}
	}

	empty, err = entityIsEmpty(ctx, d, NetworkRules)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	for _, target := range baseAllowlist {
		key, err := networkRuleKey("", target)
		if err != nil {
			return fmt.Errorf("seed the base allowlist: %w", err)
		}
		if err := putIfAbsent(ctx, d, objstore.Write{
			Op: objstore.Put, Entity: NetworkRules, Key: key,
			Attrs: map[string]string{
				attrNetCreatedAt: core.TS(allowlistAddedAt),
				attrNetCreatedBy: "system",
			},
		}); err != nil {
			return fmt.Errorf("seed the base allowlist rule %s: %w", target, err)
		}
	}
	return nil
}

// DefaultFolder is the group a new instance is born with. It is a configured
// name, the same on every instance, so a reader never derives it from a path.
const DefaultFolder = "main"

// SeedDefaultGroup writes the group a new instance is born with, and its default
// scheduled tasks, into a store that holds no group at all.
//
// It runs beside SeedRoutdFloor at routd's boot, because the rows belong to
// routd and routd is the daemon that makes them: `arizuko create` makes the
// instance's FILES — the directory, the `.env`, the compose and the group's own
// tree — and writes the operator's product choice into the environment routd
// reads it back from. An instance that already carries a group is left exactly
// as it is.
func SeedDefaultGroup(ctx context.Context, d objstore.Store, product string) error {
	empty, err := entityIsEmpty(ctx, d, Groups)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	s := NewOver(d)
	if err := s.PutGroupRow(core.Group{
		Folder: DefaultFolder, AddedAt: time.Now(), Product: product,
	}); err != nil {
		return fmt.Errorf("seed the %s group: %w", DefaultFolder, err)
	}
	if err := s.SeedDefaultTasks(DefaultFolder, DefaultFolder); err != nil {
		return fmt.Errorf("seed %s's default tasks: %w", DefaultFolder, err)
	}
	return nil
}

// entityIsEmpty reports whether one entity holds no item at all in the routd
// partition. The read is capped at one, because "is there anything" is the
// whole question.
func entityIsEmpty(ctx context.Context, s objstore.Store, entity string) (bool, error) {
	items, err := s.Query(ctx, objstore.Query{
		Entity: entity, Partition: OwnerRoutd, Limit: 1,
	})
	if err != nil {
		return false, fmt.Errorf("read %s to decide whether it needs seeding: %w", entity, err)
	}
	return len(items) == 0, nil
}

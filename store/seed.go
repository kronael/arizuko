package store

// seed.go writes the floor a fresh routd store is born with: the operator's
// root grant, the member role, and the egress rules a spawned agent needs to
// reach a model.
//
// It is code, not a migration: an entity's rows go in through its writer,
// after the driver makes the entity. It runs when the entity is EMPTY, never
// "if absent on every open": an operator who deliberately leaves a floor grant
// out of the manifest must not get it back on restart, or `arizuko plan` shows
// the same diff forever. The write is still create-if-absent, because two
// owner daemons can boot against one fresh endpoint at once. The instants are
// fixed so a fresh instance's config checksum is the same on every machine.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// RoleOperator is the delegation root (spec 5/33-paths-roles): it holds (*, **) WITH
// GRANT OPTION. Every other grant starts without the option.
const RoleOperator = "role:operator"

// memberVerbs are the messaging verbs role:member confers, at scope ** because
// containment is route ownership at runtime, not the acl scope. No grant
// option: every child is born a member directly.
var memberVerbs = []string{
	"mcp:reply", "mcp:send", "mcp:send_file", "mcp:send_voice",
	"mcp:post", "mcp:forward", "mcp:quote", "mcp:repost",
	"mcp:like", "mcp:dislike", "mcp:edit", "mcp:delete",
	"mcp:disengage",
}

// alwaysMountedTools are the READS every turn mounts, plus the memory
// checkpoint, the class spec 5/33-paths-roles exempts from grants. They need the skip row
// below, or a folder holding a blanket hold would wait for a human before it
// could read its own notes. Reads only: a write that mounts unconditionally
// (invite_create, pin_message, reset_session) stays held. routd's
// TestWorld_HoldsOnlyWhatAPersonShouldSee guards the line.
var alwaysMountedTools = []string{
	"mcp:inspect_routing", "mcp:inspect_tasks", "mcp:inspect_session",
	"mcp:inspect_identity", "mcp:inspect_messages", "mcp:find_messages",
	"mcp:get_thread", "mcp:fetch_history", "mcp:get_work", "mcp:set_work",
}

// skipApprovalFloor names every tool role:member answers WITHOUT a human. A
// world folder holds `hold:mcp:*`, and deny on a `hold:` action means SKIP
// APPROVAL (spec 6/26 § Decision 5); deny-wins beats the world's wildcard.
func skipApprovalFloor() []string {
	out := make([]string, 0, len(memberVerbs)+len(alwaysMountedTools))
	out = append(out, memberVerbs...)
	return append(out, alwaysMountedTools...)
}

// baseAllowlist is the instance-wide egress every folder inherits
// (folderAncestry). Without it a spawned container reaches no model.
var baseAllowlist = []string{"anthropic.com", "api.anthropic.com"}

// The instants the floor carries. Fixed, so two fresh instances agree.
var (
	operatorGrantedAt  = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	memberGrantedAt    = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	allowlistAddedAt   = time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	skipFloorGrantedAt = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
)

// SeedRoutdFloor writes the floor into a routd store that holds none of it. An
// instance that already carries grants or egress rules is left as it is.
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

// DefaultFolder is the group a new instance is born with: configured, never
// derived from a path.
const DefaultFolder = "main"

// SeedDefaultGroup writes the group a new instance is born with, and its
// default scheduled tasks, into a store that holds no group. It runs at routd's
// boot because routd makes the rows; `arizuko create` makes only files.
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
// partition.
func entityIsEmpty(ctx context.Context, s objstore.Store, entity string) (bool, error) {
	items, err := s.Query(ctx, objstore.Query{
		Entity: entity, Partition: OwnerRoutd, Limit: 1,
	})
	if err != nil {
		return false, fmt.Errorf("read %s to decide whether it needs seeding: %w", entity, err)
	}
	return len(items) == 0, nil
}

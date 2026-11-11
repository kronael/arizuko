package store

// seed.go writes the instance-wide egress floor.

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// RoleOperator is the delegation root (spec 5/33-paths-roles): it holds (*, **) WITH
// GRANT OPTION. Every other grant starts without the option.
const RoleOperator = "role:operator"

// baseAllowlist is the instance-wide egress every folder inherits
// (folderAncestry). Without it a spawned container reaches no model.
var baseAllowlist = []string{"anthropic.com", "api.anthropic.com"}

// The instants the floor carries. Fixed, so two fresh instances agree.
var allowlistAddedAt = time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

// SeedRoutdFloor writes the egress floor when the network rules are empty.
func SeedRoutdFloor(ctx context.Context, d objstore.Store) error {
	empty, err := entityIsEmpty(ctx, d, NetworkRules)
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
// default scheduled tasks, into a store that holds no group. It runs at
// routd's boot because routd makes the rows; `arizuko create` makes only files.
func SeedDefaultGroup(ctx context.Context, d objstore.Store, product string) error {
	empty, err := entityIsEmpty(ctx, d, Groups)
	if err != nil || !empty {
		return err
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

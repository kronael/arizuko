package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kronael/arizuko/objstore"
)

// MigrateGitHubSourceRoutes binds unambiguous default-owner routes once.
func (s *Store) MigrateGitHubSourceRoutes(ctx context.Context) error {
	marker := objstore.Key{Partition: OwnerRoutd, Sort: "github-source-route-ownership"}
	items, err := s.obj.Query(ctx, objstore.Query{Entity: Counters, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: marker.Sort}})
	if err != nil || len(items) != 0 {
		return err
	}
	sources, err := s.obj.Query(ctx, objstore.Query{Entity: GitHubSources, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrGHRouteIDs, Op: objstore.Absent}}})
	if err != nil {
		return err
	}
	routes, err := s.allRoutes(ctx)
	if err != nil {
		return err
	}
sources:
	for _, source := range sources {
		owner := source.Attrs[attrGHOwnerFolder]
		seeded, err := GitHubSourceRoutes(source.Key.Sort, owner)
		if err != nil {
			slog.Warn("github source ownership migration skipped", "source", source.Key.Sort, "reason", err)
			continue
		}
		ids := make([]int64, len(seeded))
		for i, seed := range seeded {
			n := 0
			for _, route := range routes {
				if route.Match == seed.Match && route.Target == seed.Target {
					ids[i] = route.ID
					n++
				}
			}
			if n != 1 {
				slog.Warn("github source ownership migration skipped", "source", source.Key.Sort,
					"reason", fmt.Sprintf("match %q target %q: found %d routes, want exactly one", seed.Match, seed.Target, n))
				continue sources
			}
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		ok, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: GitHubSources, Key: source.Key,
			Attrs: map[string]string{attrGHTargetFolder: owner, attrGHRouteIDs: string(encoded)},
			Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
				{Attr: attrGHRouteIDs, Op: objstore.Absent},
				{Attr: attrGHOwnerFolder, Op: objstore.Eq, Value: owner},
			}},
		})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("github source ownership migration conflict: %s", source.Key.Sort)
		}
	}
	return s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Put, Entity: Counters, Key: marker,
		Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent}})
}

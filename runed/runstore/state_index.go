package runstore

import (
	"context"
	"slices"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type spawnIndex struct{}

func (spawnIndex) Indexed(entity string) bool { return entity == Spawns }
func (spawnIndex) Keys(_ string, it objstore.Item) []objstore.Key {
	var keys []objstore.Key
	if at, err := time.Parse(time.RFC3339Nano, it.Attrs[attrCreatedAt]); err == nil {
		keys = append(keys, objstore.DueIndexKey("spawns/expiry", core.TS(at), Spawns, it.Key))
	}
	if state, exists := it.Attrs[attrState]; exists && !slices.Contains(terminalStates, state) {
		keys = append(keys, objstore.Key{Partition: "spawns/live", Sort: it.Key.Sort})
		if it.Attrs[attrKind] == KindAgent {
			keys = append(keys, objstore.Key{Partition: "spawns/live_agent", Sort: it.Key.Sort})
		}
	}
	return keys
}
func (s *Store) MigrateStateIndexes(ctx context.Context) error {
	return objstore.Reindex(ctx, s.s, "spawns-v1", []objstore.Query{{Entity: Spawns, Partition: spawnPartition}})
}

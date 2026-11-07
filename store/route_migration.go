package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// MigrateRouteModes moves legacy mode fragments to URI query flags once.
func (s *Store) MigrateRouteModes(ctx context.Context) error {
	marker := objstore.Key{Partition: OwnerRoutd, Sort: "route-target-uri"}
	items, err := s.obj.Query(ctx, objstore.Query{Entity: Counters, Partition: OwnerRoutd, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: marker.Sort}})
	if err != nil || len(items) != 0 {
		return err
	}
	items, err = s.obj.Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		return err
	}
	for _, item := range items {
		route, err := routeFrom(item)
		if err != nil {
			return err
		}
		folder, mode, found := strings.Cut(route.Target, "#")
		if !found || (mode != "observe" && mode != "engaged" && mode != "announce" && mode != "issues") {
			continue
		}
		route.Target = folder + "?" + mode
		key, err := routeKey(route)
		if err != nil {
			return err
		}
		ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{
			{Op: objstore.Delete, Entity: Routes, Key: item.Key, Cond: objstore.Cond{Item: objstore.ItemPresent}},
			{Op: objstore.Put, Entity: Routes, Key: key, Attrs: item.Attrs, Cond: objstore.Cond{Item: objstore.ItemAbsent}},
		})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("route migration conflict: %s", route.Target)
		}
	}
	ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Put, Entity: Counters, Key: marker, Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent}}})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("route migration marker conflict")
	}
	return nil
}

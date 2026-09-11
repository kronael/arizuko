package store

// group_watchers.go holds every read and write of `group_watchers`: one item per
// (observer, source) subscription, which is what makes a source folder's inbound
// messages arrive in the observer's prompt as ambient context.
//
// THE PARTITION IS OwnerRoutd AND THE OBSERVER LEADS THE SORT KEY. Both writes
// name the whole pair — subscribe and cancel — and the one read names the
// observer alone, so with the observer leading it is a begins_with over a
// leading fragment of the key. Putting the source first would make that read a
// whole-partition scan with a filter.
//
// THE ITEM CARRIES NO ATTRIBUTES. The pair IS the fact; there is nothing else to
// record about a subscription, and a stamp nobody reads would be a second thing
// to keep true.
//
// NEITHER FOLDER CAN BE THE PARTITION, and this entity is the one where that is
// obvious: a group delete purges by observer AND by source, so whichever half
// were the partition, the other half's purge would have no partition to name —
// and enumerating partitions is not an operation the interface has. Under the
// constant BOTH purges are a whole-partition read with the containment test done
// here, which is what every other entity's purge already does
// (folder_scope.go). The table holds no rows on any of the three live instances
// (2026-09-09); what bounds it is the number of live observe_group
// subscriptions.
//
// NOTHING HERE IS COMPARED BY ORDER. There is no timestamp and no counter, and
// both fields are folder paths compared by equality and by containment. A
// containment test is a prefix, not an ordering, so no attribute carries a width
// declaration.
//
// SUBSCRIBING IS IDEMPOTENT AND NOT A CLAIM. No caller reads whether the write
// was the first: the agent's `observe_group` tool asks for the subscription to
// exist, and it does. So a refused create is success here, where in
// turn_results.go the identical shape is the whole answer.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// watcherKey is the coordinate of one subscription.
func watcherKey(observer, source string) (objstore.Key, error) {
	if observer == "" || source == "" {
		return objstore.Key{}, fmt.Errorf("group watcher carries observer=%q source=%q, and "+
			"both are its coordinate", observer, source)
	}
	for name, v := range map[string]string{"observer": observer, "source": source} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("group watcher %s=%q carries the separator that "+
				"divides the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: observer + KeySep + source}, nil
}

// watcherCoord splits a sort key back into (observer, source). A key that does
// not split is a LOUD fault: it names an item nobody wrote.
func watcherCoord(sortKey string) (observer, source string, err error) {
	observer, source, ok := strings.Cut(sortKey, KeySep)
	if !ok {
		return "", "", fmt.Errorf("group watcher %q does not split into (observer, source)",
			sortKey)
	}
	return observer, source, nil
}

// AddGroupWatcher subscribes observer to source's inbound messages as ambient
// context (spec 5/F). Asking twice is asking for the same state, so a create the
// item's own presence refuses is success and not an error.
func (s *Store) AddGroupWatcher(observer, source string) error {
	key, err := watcherKey(observer, source)
	if err != nil {
		return err
	}
	// A refusal is the subscription already being there, which is the state the
	// caller asked for.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: GroupWatchers, Key: key, Attrs: map[string]string{},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}); err != nil {
		return fmt.Errorf("subscribe %s to %s: %w", observer, source, err)
	}
	return nil
}

// RemoveGroupWatcher cancels the subscription. Cancelling one that is not there
// is not an error: the caller asks for the subscription to be gone, and it is.
func (s *Store) RemoveGroupWatcher(observer, source string) error {
	key, err := watcherKey(observer, source)
	if err != nil {
		return err
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Delete, Entity: GroupWatchers, Key: key,
	}); err != nil {
		return fmt.Errorf("unsubscribe %s from %s: %w", observer, source, err)
	}
	return nil
}

// WatchedSources answers the source folders observer is subscribed to, which the
// prompt build splices into its ambient window as a second `messages` read.
//
// The read is a begins_with on the observer, so it costs the subscriptions this
// observer holds and no others.
func (s *Store) WatchedSources(observer string) []string {
	sources, err := s.watchedSources(context.Background(), observer)
	if err != nil {
		return nil
	}
	return sources
}

func (s *Store) watchedSources(ctx context.Context, observer string) ([]string, error) {
	if observer == "" || strings.Contains(observer, KeySep) {
		return nil, fmt.Errorf("observer %q is not half a group-watcher coordinate", observer)
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: GroupWatchers, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: observer + KeySep},
	})
	if err != nil {
		return nil, fmt.Errorf("read what %s watches: %w", observer, err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		_, source, cerr := watcherCoord(it.Key.Sort)
		if cerr != nil {
			return nil, cerr
		}
		out = append(out, source)
	}
	return out, nil
}

// CountGroupWatchers answers how many subscriptions exist. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountGroupWatchers(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: GroupWatchers, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the group watchers: %w", err)
	}
	return len(items), nil
}

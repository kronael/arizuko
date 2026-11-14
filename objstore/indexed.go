package objstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

const IndexEntity = "state_index"
const IndexBatchSize = 10
const indexRevision = "_index_revision"

type Indexer interface {
	Indexed(entity string) bool
	Keys(entity string, item Item) []Key
}

type Indexed struct {
	Store
	Indexer
}

type indexBatchContext struct{}

func WithIndexes(s Store, index Indexer) Store {
	if _, ok := s.(*Indexed); ok {
		return s
	}
	return &Indexed{Store: s, Indexer: index}
}

func (s *Indexed) GuardedBatch(ctx context.Context, checks []Check, writes []Write) (bool, error) {
	if err := ValidateBatch(checks, writes); err != nil {
		return false, err
	}
	if ctx.Value(indexBatchContext{}) == &writes[0] {
		return s.Store.GuardedBatch(ctx, checks, writes)
	}
	for range 16 {
		batch := slices.Clone(writes)
		before := map[int]Item{}
		for i, w := range writes {
			if !s.Indexed(w.Entity) {
				continue
			}
			old, exists, err := indexPoint(ctx, s.Store, w.Entity, w.Key)
			if err != nil {
				return false, err
			}
			if w.Op == Update && !exists || w.Cond.Item == ItemAbsent && exists {
				return false, nil
			}
			before[i] = old
			next := maps.Clone(old.Attrs)
			if w.Op == Put || next == nil {
				next = map[string]string{}
			}
			if !exists && w.Op == Merge {
				maps.Copy(next, w.Init)
			}
			maps.Copy(next, w.Attrs)
			for _, name := range w.Remove {
				delete(next, name)
			}
			guard := Cmp{Attr: indexRevision, Op: Absent}
			if revision := old.Attrs[indexRevision]; revision != "" {
				guard.Op, guard.Value = Eq, revision
			}
			w.Cond.Where = append(slices.Clone(w.Cond.Where), guard)
			w.Init, w.Remove = nil, nil
			w.Attrs = next
			w.Attrs[indexRevision] = rand.Text()
			if w.Op != Delete {
				if exists {
					w.Op = Update
					for name := range old.Attrs {
						if _, kept := next[name]; !kept {
							w.Remove = append(w.Remove, name)
						}
					}
				} else {
					w.Op = Put
					w.Cond = Cond{Item: ItemAbsent}
				}
			}
			batch[i] = w
			var was, now []Key
			if exists {
				was = s.Keys(w.Entity, old)
			}
			if w.Op != Delete {
				now = s.Keys(w.Entity, Item{Key: w.Key, Attrs: next})
			}
			for _, key := range was {
				if !slices.Contains(now, key) {
					batch = append(batch, Write{Op: Delete, Entity: IndexEntity,
						Key: indexKey(key, w.Entity, w.Key)})
				}
			}
			for _, key := range now {
				batch = append(batch, Write{Op: Put, Entity: IndexEntity,
					Key: indexKey(key, w.Entity, w.Key), Attrs: map[string]string{
						"entity": w.Entity, "source_partition": w.Key.Partition, "source_sort": w.Key.Sort,
					}})
			}
		}
		ok, err := s.Store.GuardedBatch(context.WithValue(ctx, indexBatchContext{}, &batch[0]), checks, batch)
		if err != nil || ok {
			return ok, err
		}
		changed := false
		for i, old := range before {
			current, _, err := indexPoint(ctx, s.Store, writes[i].Entity, writes[i].Key)
			if err != nil {
				return false, err
			}
			changed = changed || current.Attrs[indexRevision] != old.Attrs[indexRevision]
		}
		if !changed {
			return false, nil
		}
	}
	return false, fmt.Errorf("index write changed during 16 attempts")
}

func indexKey(key Key, entity string, source Key) Key {
	data, _ := json.Marshal([3]string{entity, source.Partition, source.Sort})
	key.Sort += "\x1f" + base64.RawURLEncoding.EncodeToString(data)
	return key
}

func indexPoint(ctx context.Context, s Store, entity string, key Key) (Item, bool, error) {
	rows, err := s.Query(ctx, Query{Entity: entity, Partition: key.Partition,
		Key: KeyPredicate{Op: ExactKey, Value: key.Sort}})
	if err != nil || len(rows) == 0 {
		return Item{Key: key}, false, err
	}
	return rows[0], true, nil
}

func IndexItems(ctx context.Context, s Store, partition string, key KeyPredicate, limit int) ([]Item, error) {
	refs, err := s.Query(ctx, Query{Entity: IndexEntity, Partition: partition, Key: key, Limit: limit})
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(refs))
	for _, ref := range refs {
		it, exists, err := indexPoint(ctx, s, ref.Attrs["entity"],
			Key{Partition: ref.Attrs["source_partition"], Sort: ref.Attrs["source_sort"]})
		if err != nil {
			return nil, err
		}
		if exists {
			items = append(items, it)
		}
	}
	return items, nil
}

// Reindex runs before the owner's workers start. The marker commits only
// after every row has its pointers, so an interrupted run can resume.
func Reindex(ctx context.Context, s Store, marker string, partitions []Query) error {
	key := Key{Partition: "migrations", Sort: marker}
	_, done, err := indexPoint(ctx, s, IndexEntity, key)
	if err != nil || done {
		return err
	}
	for _, q := range partitions {
		rows, err := s.Query(ctx, q)
		if err != nil {
			return err
		}
		for _, it := range rows {
			ok, err := s.GuardedBatch(ctx, nil, []Write{{Op: Update, Entity: q.Entity, Key: it.Key}})
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("reindex %s/%s: row changed", q.Entity, it.Key.Sort)
			}
		}
	}
	_, err = s.GuardedBatch(ctx, nil, []Write{{Op: Put, Entity: IndexEntity, Key: key,
		Attrs: map[string]string{"done": "1"}, Cond: Cond{Item: ItemAbsent}}})
	return err
}

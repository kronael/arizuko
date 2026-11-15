package objstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
)

const IndexEntity = "state_index"
const IndexBatchSize = 8
const reindexProgressRows = 1000
const IndexRevision = "_index_revision"

type Indexer interface {
	Indexed(entity string) bool
	// Keys returns complete, unique pointer coordinates.
	Keys(entity string, item Item) []Key
}

// CompanionIndexer schedules coalesced work alongside a primary transition.
// Companions are unconditional merges into separate, indexed work records.
type CompanionIndexer interface {
	Companions(entity string, before, after Item) []Write
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
		expanded := slices.Clone(writes)
		var batch []Write
		companions := map[[3]string]int{}
		before := map[int]Item{}
		for i := 0; i < len(expanded); i++ {
			w := expanded[i]
			if !s.Indexed(w.Entity) {
				batch = append(batch, w)
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
			guard := Cmp{Attr: IndexRevision, Op: Absent}
			if revision := old.Attrs[IndexRevision]; revision != "" {
				guard.Op, guard.Value = Eq, revision
			}
			w.Cond.Where = append(slices.Clone(w.Cond.Where), guard)

			next[IndexRevision] = rand.Text()
			w.Init = nil
			if w.Op != Delete {
				if exists && w.Op != Put {
					w.Attrs = maps.Clone(w.Attrs)
					if w.Attrs == nil {
						w.Attrs = map[string]string{}
					}
				} else {
					w.Attrs = next
					w.Remove = nil
					if exists {
						for name := range old.Attrs {
							if _, kept := next[name]; !kept {
								w.Remove = append(w.Remove, name)
							}
						}
					}
				}
				w.Attrs[IndexRevision] = next[IndexRevision]
				w.Op = Update
				if !exists {
					w.Op = Put
					w.Cond = Cond{Item: ItemAbsent}
				}
			}
			batch = append(batch, w)
			after := Item{Key: w.Key, Attrs: next}
			if w.Op == Delete {
				after = Item{}
			}
			if !exists {
				old = Item{}
			}
			batch = append(batch, IndexChanges(s.Indexer, w.Entity, old, after)...)
			if index, ok := s.Indexer.(CompanionIndexer); ok {
				for _, companion := range index.Companions(w.Entity, old, after) {
					if companion.Op != Merge || len(companion.Remove) != 0 {
						return false, fmt.Errorf("index companion must merge work")
					}
					if err := validateWrite(companion); err != nil {
						return false, err
					}
					id := [3]string{companion.Entity, companion.Key.Partition, companion.Key.Sort}
					if j, exists := companions[id]; exists {
						if j <= i {
							return false, fmt.Errorf("cyclic index companion")
						}
						if expanded[j].Attrs == nil {
							expanded[j].Attrs = map[string]string{}
						}
						maps.Copy(expanded[j].Attrs, companion.Attrs)
						continue
					}
					for _, primary := range writes {
						if primary.Entity == companion.Entity && primary.Key == companion.Key {
							return false, fmt.Errorf("index companion also supplied as primary")
						}
					}
					companions[id] = len(expanded)
					expanded = append(expanded, companion)
				}
			}

		}
		ok, err := s.Store.GuardedBatch(context.WithValue(ctx, indexBatchContext{}, &batch[0]), checks, batch)
		if err != nil || ok {
			return ok, err
		}
		changed := false
		for i, old := range before {
			current, _, err := indexPoint(ctx, s.Store, expanded[i].Entity, expanded[i].Key)
			if err != nil {
				return false, err
			}
			changed = changed || current.Attrs[IndexRevision] != old.Attrs[IndexRevision]
		}
		if !changed {
			return false, nil
		}
	}
	return false, fmt.Errorf("index write changed during 16 attempts")
}

// IndexChanges derives pointers for an externally guarded primary transition.
// Journaled maintenance supplies exact before/after images without changing them.
func IndexChanges(index Indexer, entity string, before, after Item) []Write {
	if !index.Indexed(entity) {
		return nil
	}
	var was, now []Key
	if before.Key != (Key{}) {
		was = index.Keys(entity, before)
	}
	if after.Key != (Key{}) {
		now = index.Keys(entity, after)
	}
	var writes []Write
	for _, key := range was {
		if !slices.Contains(now, key) {
			writes = append(writes, Write{Op: Delete, Entity: IndexEntity, Key: key})
		}
	}
	for _, key := range now {
		writes = append(writes, Write{Op: Put, Entity: IndexEntity,
			Key: key, Attrs: map[string]string{
				"entity": entity, "source_partition": after.Key.Partition, "source_sort": after.Key.Sort,
			}})
	}
	return writes
}

// DueIndexKey gives equal deadlines distinct, bounded source identities.
func DueIndexKey(partition, deadline, entity string, source Key) Key {
	data, _ := json.Marshal([3]string{entity, source.Partition, source.Sort})
	return Key{Partition: partition, Sort: fmt.Sprintf("%s\x1f%x", deadline, sha256.Sum256(data))}
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
	return QueryIndex(ctx, s, Query{Partition: partition, Key: key, Limit: limit})
}

func QueryIndex(ctx context.Context, s Store, q Query) ([]Item, error) {
	q.Entity = IndexEntity
	refs, err := s.Query(ctx, q)
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
// after every row has its pointers, so an interrupted run can resume. It logs
// its start, every reindexProgressRows rows and its end (BUGS L888).
func Reindex(ctx context.Context, s Store, marker string, partitions []Query) error {
	key := Key{Partition: "migrations", Sort: marker}
	_, done, err := indexPoint(ctx, s, IndexEntity, key)
	if err != nil || done {
		return err
	}
	slog.Info("reindex start", "marker", marker)
	total := 0
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
			if total++; total%reindexProgressRows == 0 {
				slog.Info("reindex progress", "marker", marker, "rows", total)
			}
		}
	}
	slog.Info("reindex end", "marker", marker, "rows", total)
	_, err = s.GuardedBatch(ctx, nil, []Write{{Op: Put, Entity: IndexEntity, Key: key,
		Attrs: map[string]string{"done": "1"}, Cond: Cond{Item: ItemAbsent}}})
	return err
}

package conformance

// helpers.go holds what the bodies say in shorthand. Nothing here asserts; a
// helper that failed a test would hide which behaviour broke.

import (
	"context"
	"sort"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// claim is authd's ClaimRefreshToken as one write: set used_at, but only on an
// item that carries neither tombstone.
//
// Both conjuncts are load-bearing and each closes a different half of BUGS F36.
// used_at Absent picks the single winner out of N concurrent redeems.
// revoked_at Absent refuses a token whose family died between the caller's
// lookup and this write — a revoke that commits BEFORE the batch opens is not
// an interleave, so the claim itself has to look.
func claim(key objstore.Key) objstore.Write {
	return objstore.Write{
		Op: objstore.Update, Entity: tokens, Key: key,
		Attrs: map[string]string{"used_at": core.NowTS()},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: "used_at", Op: objstore.Absent},
			{Attr: "revoked_at", Op: objstore.Absent},
		}},
	}
}

// put stores one item.
func put(t *testing.T, s objstore.Store, entity string, key objstore.Key, attrs map[string]string) {
	t.Helper()
	committed, err := s.GuardedBatch(context.Background(), nil, []objstore.Write{
		{Op: objstore.Put, Entity: entity, Key: key, Attrs: attrs},
	})
	if err != nil {
		t.Fatalf("put %s/%s: %v", entity, key.Sort, err)
	}
	if !committed {
		t.Fatalf("put %s/%s was refused; an unconditional put has nothing to refuse", entity, key.Sort)
	}
}

// putAll stores many items, in batches no larger than one transaction holds. A
// body that needs a selection larger than any engine's own page needs this; a
// body that wrote them one at a time would spend its runtime on round trips.
func putAll(t *testing.T, s objstore.Store, entity string, items []objstore.Item) {
	t.Helper()
	for len(items) > 0 {
		n := min(len(items), objstore.MaxBatchActions)
		writes := make([]objstore.Write, n)
		for i, it := range items[:n] {
			writes[i] = objstore.Write{Op: objstore.Put, Entity: entity, Key: it.Key, Attrs: it.Attrs}
		}
		committed, err := s.GuardedBatch(context.Background(), nil, writes)
		if err != nil {
			t.Fatalf("put %d items into %s: %v", n, entity, err)
		}
		if !committed {
			t.Fatalf("a bulk put into %s was refused", entity)
		}
		items = items[n:]
	}
}

// commit runs one write and reports whether it committed. An ERROR fails the
// test on the spot: a refusal and a fault are different answers, and a body that
// let one stand for the other would prove nothing.
func commit(t *testing.T, s objstore.Store, ctx context.Context, w objstore.Write) bool {
	t.Helper()
	committed, err := s.GuardedBatch(ctx, nil, []objstore.Write{w})
	if err != nil {
		t.Fatalf("write %s/%s: %v", w.Entity, w.Key.Sort, err)
	}
	return committed
}

// mustCommit runs one write and fails unless it commits.
func mustCommit(t *testing.T, s objstore.Store, ctx context.Context, w objstore.Write) {
	t.Helper()
	if !commit(t, s, ctx, w) {
		t.Fatalf("write %s/%s was refused", w.Entity, w.Key.Sort)
	}
}

// point reads one item by its exact coordinate, applying no predicate of any
// kind — the shape of authd's RefreshTokenBySecret.
func point(t *testing.T, s objstore.Store, entity string, key objstore.Key) []objstore.Item {
	t.Helper()
	items, err := s.Query(context.Background(), objstore.Query{
		Entity: entity, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		t.Fatalf("read %s/%s: %v", entity, key.Sort, err)
	}
	return items
}

// one is point where the item must be there.
func one(t *testing.T, s objstore.Store, entity string, key objstore.Key) objstore.Item {
	t.Helper()
	items := point(t, s, entity, key)
	if len(items) != 1 {
		t.Fatalf("read %s/%s returned %d items, want 1", entity, key.Sort, len(items))
	}
	return items[0]
}

// s2keys runs a query and returns the sort keys it selected, in the order the
// driver returned them.
func s2keys(t *testing.T, s objstore.Store, q objstore.Query) []string {
	t.Helper()
	items, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("query %s: %v", q.Entity, err)
	}
	return keysOf(items)
}

// keysOf lists the sort keys of some items, in the order they came.
func keysOf(items []objstore.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key.Sort
	}
	return out
}

// sameKeys compares two key sets regardless of order.
func sameKeys(got, want []string) bool {
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return sameOrder(g, w)
}

// sameOrder compares two key lists as sequences.
func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// reversed returns keys back to front.
func reversed(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[len(keys)-1-i] = k
	}
	return out
}

package main

// store_fixture_test.go is how a test reaches authd's entities directly: to
// build a state the daemon has no writer for, and to read back what a write
// actually put there.
//
// It goes through the driver rather than through statements. A fixture that
// spelled the storage out in SQL would be asserting on ONE driver's table shape
// while the daemon it tests holds none, so the day a second driver runs these
// bodies the fixtures would be the only thing left needing a rewrite.

import (
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// newTestStore is the storage layer over a test database, for the bodies that
// exercise authstore directly instead of through the daemon.
func newTestStore(db *sql.DB) *authstore.Store { return authstore.New(sqlstore.New(db)) }

// entityItems reads every item of one entity, narrowed by the comparisons given.
func entityItems(t *testing.T, db *sql.DB, entity string, where ...objstore.Cmp) []objstore.Item {
	t.Helper()
	items, err := sqlstore.New(db).Query(t.Context(), objstore.Query{
		Entity: entity, Partition: authstore.Partition, Where: where,
	})
	if err != nil {
		t.Fatalf("read %s: %v", entity, err)
	}
	return items
}

// entityItem reads one item by its coordinate.
func entityItem(t *testing.T, db *sql.DB, entity, sort string) (objstore.Item, bool) {
	t.Helper()
	items, err := sqlstore.New(db).Query(t.Context(), objstore.Query{
		Entity: entity, Partition: authstore.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: sort},
	})
	if err != nil {
		t.Fatalf("read %s/%s: %v", entity, sort, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false
	}
	return items[0], true
}

// entityCount counts an entity's items.
func entityCount(t *testing.T, db *sql.DB, entity string) int {
	t.Helper()
	return len(entityItems(t, db, entity))
}

// putItem writes one item outright, which is what a fixture that has to
// manufacture a state the daemon never writes needs.
func putItem(t *testing.T, db *sql.DB, entity, sort string, attrs map[string]string) {
	t.Helper()
	committed, err := sqlstore.New(db).GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: entity,
		Key:   objstore.Key{Partition: authstore.Partition, Sort: sort},
		Attrs: attrs,
	}})
	if err != nil || !committed {
		t.Fatalf("write %s/%s: committed=%v err=%v", entity, sort, committed, err)
	}
}

// mergeAttrs sets attributes on an item that already exists.
func mergeAttrs(t *testing.T, db *sql.DB, entity, sort string, attrs map[string]string) {
	t.Helper()
	committed, err := sqlstore.New(db).GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: entity,
		Key:   objstore.Key{Partition: authstore.Partition, Sort: sort},
		Attrs: attrs,
		Cond:  objstore.Cond{Item: objstore.ItemPresent},
	}})
	if err != nil || !committed {
		t.Fatalf("update %s/%s: committed=%v err=%v", entity, sort, committed, err)
	}
}

// activeKid is the kid the signing slot names, empty when no key holds it. The
// slot IS the answer to "which key signs" — there is no per-key flag to count,
// which is what makes two signers unrepresentable.
func activeKid(t *testing.T, db *sql.DB) string {
	t.Helper()
	it, ok := entityItem(t, db, authstore.SigningKeySlot, "active")
	if !ok {
		return ""
	}
	return it.Attrs["kid"]
}

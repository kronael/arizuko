package sqlstore_test

// The SQL driver runs the conformance suite, and which ENGINE it runs against
// is ARIZUKO_TEST_PG_DSN — the one variable db_utils already uses to point the
// whole suite at Postgres (spec 6/37 step 6). Unset, this is SQLite and stays
// inside `make test`'s budget; set, the identical bodies run on Postgres.

import (
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// newStore is the suite's Factory over this driver.
func newStore(t *testing.T, entities ...string) objstore.Store {
	t.Helper()
	s := sqlstore.New(dbtest.Open(t))
	for _, e := range entities {
		if err := s.CreateEntity(context.Background(), e); err != nil {
			t.Fatalf("create entity %s: %v", e, err)
		}
	}
	return s
}

func TestConformance(t *testing.T) { conformance.Run(t, newStore) }

// An entity is a table name and an attribute is a key inside a JSON path, so
// neither can be a bind parameter and both are written into the statement. The
// validator is what makes that safe, and this is the test that it is not
// decorative.
func TestGeneratedNamesAreRefusedUnlessTheyAreIdentifiers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, "routes")
	for _, bad := range []string{
		"routes; DROP TABLE routes--",
		"routes'",
		"1routes",
		"ROUTES",
		"",
	} {
		if _, err := s.Query(ctx, objstore.Query{Entity: bad, Partition: "acme"}); err == nil {
			t.Errorf("entity %q was accepted", bad)
		}
	}
	for _, bad := range []string{"a'||(SELECT 1)||'", "$.x", "Used_At", ""} {
		_, err := s.Query(ctx, objstore.Query{
			Entity: "routes", Partition: "acme",
			Where: []objstore.Cmp{{Attr: bad, Op: objstore.Present}},
		})
		if err == nil {
			t.Errorf("attribute %q was accepted in a filter", bad)
		}
		if _, err = s.Query(ctx, objstore.Query{
			Entity: "routes", Partition: "acme", Project: []string{bad},
		}); err == nil {
			t.Errorf("attribute %q was accepted in a projection", bad)
		}
	}
	var one int
	if err := dbtest.Open(t).QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
}

// A partition is a bound VALUE, never generated text, so it needs no validator
// and must survive anything a caller puts in it.
func TestPartitionAndSortAreValuesRatherThanText(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, "routes")
	nasty := objstore.Key{Partition: "acme'; DROP TABLE routes--", Sort: `a" OR 1=1 --`}
	committed, err := s.GuardedBatch(ctx, nil, []objstore.Write{
		{Op: objstore.Put, Entity: "routes", Key: nasty, Attrs: map[string]string{"seq": "1"}},
	})
	if err != nil || !committed {
		t.Fatalf("put with a hostile key: committed=%v err=%v", committed, err)
	}
	got, err := s.Query(ctx, objstore.Query{
		Entity: "routes", Partition: nasty.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: nasty.Sort},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != nasty {
		t.Fatalf("read back %v, want the item written at %v", got, nasty)
	}
	if strings.Contains(got[0].Attrs["seq"], "DROP") {
		t.Fatal("the key reached the attributes")
	}
}

// A service's migration files are frozen once applied, so a chain that created
// a table before it became an entity goes on creating it in every database born
// afterwards. CreateEntity takes the name when there is nothing in the way, and
// refuses when there is — and the two cases differ only in whether the table
// holds a row, so both are tested against the same legacy shape.
func TestAnEmptyTableOfAnotherShapeYieldsTheNameAndAFullOneDoesNot(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	if _, err := db.Exec(`CREATE TABLE turn_context (turn_id TEXT PRIMARY KEY, state TEXT)`); err != nil {
		t.Fatal(err)
	}
	s := sqlstore.New(db)
	if err := s.CreateEntity(ctx, "turn_context"); err != nil {
		t.Fatalf("an empty legacy table blocked the entity: %v", err)
	}
	committed, err := s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: "turn_context",
		Key:   objstore.Key{Partition: "routd", Sort: "t1"},
		Attrs: map[string]string{"state": "running"},
	}})
	if err != nil || !committed {
		t.Fatalf("the entity that took the name does not serve writes: committed=%v err=%v",
			committed, err)
	}
}

// The refusal is what protects rows a migration tool has not carried yet, so it
// has to survive the reclaim above: a legacy table with a row in it keeps its
// name, and the caller is told which tool moves it.
func TestALegacyTableWithRowsKeepsItsNameAndSaysSo(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	if _, err := db.Exec(`CREATE TABLE turn_context (turn_id TEXT PRIMARY KEY, state TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turn_context (turn_id, state) VALUES ('t1', 'running')`); err != nil {
		t.Fatal(err)
	}
	err := sqlstore.New(db).CreateEntity(ctx, "turn_context")
	if err == nil {
		t.Fatal("a legacy table holding a row was taken over; its row would be unreadable")
	}
	if !strings.Contains(err.Error(), "migrate-") {
		t.Fatalf("the refusal does not name the tool that moves the rows: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_context`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the legacy row did not survive the refusal: n=%d err=%v", n, err)
	}
}

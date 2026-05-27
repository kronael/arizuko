package resreg

// Engine isolation tests — no arizuko-specific resource. Uses a synthetic
// `TestResource` struct + an in-memory SQLite to exercise scan/insert/
// delete/parse/emit/apply round-trips. Per spec 5/36 §"Testability".

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"gopkg.in/yaml.v3"
)

// TestRow is the synthetic schema. Composite PK on (kind, name); scope
// is "kind" so per-scope delete works. Mirrors the shape of acl_membership
// without depending on any arizuko code.
type TestRow struct {
	Kind  string `db:"kind"  yaml:"kind"`
	Name  string `db:"name"  yaml:"name"`
	Value string `db:"value" yaml:"value"`
}

const testSchema = `
CREATE TABLE config_meta (version INTEGER NOT NULL DEFAULT 0);
INSERT INTO config_meta (version) VALUES (0);
CREATE TABLE testrows (
  kind  TEXT NOT NULL,
  name  TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (kind, name)
);
`

// freshEngine resets the package registry, registers TestRow, and
// returns an in-memory SQLite with schema applied.
func freshEngine(t *testing.T) (*sql.DB, *Resource) {
	t.Helper()
	reset()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	r := Register(Resource{
		Name:     "testrows",
		Table:    "testrows",
		RowType:  reflect.TypeOf(TestRow{}),
		PKFields: []string{"Kind", "Name"},
		Scope:    ScopeSpec{Field: "Kind"},
	})
	return db, r
}

func insertRaw(t *testing.T, db *sql.DB, rows ...TestRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO testrows (kind, name, value) VALUES (?, ?, ?)`,
			r.Kind, r.Name, r.Value); err != nil {
			t.Fatalf("insert raw: %v", err)
		}
	}
}

func TestScanAll_RoundTrip(t *testing.T) {
	db, r := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	got, err := r.ScanAll(db)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows, ok := got.([]TestRow)
	if !ok {
		t.Fatalf("ScanAll returned %T, want []TestRow", got)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
	// orderBy PK → (a,x), (a,y), (b,z)
	if rows[0].Name != "x" || rows[1].Name != "y" || rows[2].Name != "z" {
		t.Errorf("order = %v, want x,y,z", rows)
	}
}

func TestInsert_PlaceholderOrder(t *testing.T) {
	db, r := freshEngine(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if err := r.Insert(context.Background(), tx,
		TestRow{Kind: "a", Name: "x", Value: "v"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var k, n, v string
	if err := db.QueryRow(`SELECT kind, name, value FROM testrows`).Scan(&k, &n, &v); err != nil {
		t.Fatalf("query: %v", err)
	}
	if k != "a" || n != "x" || v != "v" {
		t.Errorf("got (%q,%q,%q), want (a,x,v)", k, n, v)
	}
}

func TestDeleteScope_CompositePK(t *testing.T) {
	db, r := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	tx, _ := db.Begin()
	if err := r.DeleteScope(context.Background(), tx, "a"); err != nil {
		t.Fatalf("DeleteScope: %v", err)
	}
	tx.Commit()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&n)
	if n != 1 {
		t.Errorf("count = %d, want 1 (only kind=b left)", n)
	}
}

func TestParseRows_RoundTrip(t *testing.T) {
	_, r := freshEngine(t)
	yamlIn := `
- kind: a
  name: x
  value: "1"
- kind: b
  name: z
  value: "3"
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(yamlIn), &node); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	got, err := r.ParseRows(&node)
	if err != nil {
		t.Fatalf("ParseRows: %v", err)
	}
	rows := got.([]TestRow)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	if rows[0].Kind != "a" || rows[1].Kind != "b" {
		t.Errorf("rows = %v", rows)
	}
}

func TestYAMLEmit_Deterministic(t *testing.T) {
	db, _ := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "b", Name: "z", Value: "3"},
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
	)
	m1, err := Export(db)
	if err != nil {
		t.Fatalf("Export 1: %v", err)
	}
	b1, err := EmitYAML(m1)
	if err != nil {
		t.Fatalf("Emit 1: %v", err)
	}
	m2, err := Export(db)
	if err != nil {
		t.Fatalf("Export 2: %v", err)
	}
	b2, err := EmitYAML(m2)
	if err != nil {
		t.Fatalf("Emit 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("non-deterministic emit:\n--- 1 ---\n%s\n--- 2 ---\n%s", b1, b2)
	}
	// row order in the emitted yaml should be (a,x), (a,y), (b,z) by PK.
	// yaml.v3 sometimes quotes "y" as `"y"` since it looks like a bool — strip quotes for the substring search.
	out := string(b1)
	ix := strings.Index(out, "value: \"1\"")
	iy := strings.Index(out, "value: \"2\"")
	iz := strings.Index(out, "value: \"3\"")
	if !(ix >= 0 && iy > ix && iz > iy) {
		t.Errorf("rows not PK-sorted in emit:\n%s", out)
	}
}

func TestApply_VersionMismatch(t *testing.T) {
	db, _ := freshEngine(t)
	// db is at version 0; tell Apply manifest is at version 42 → mismatch.
	_, err := Apply(context.Background(), db, 42, false, map[string]any{
		"testrows": []TestRow{{Kind: "a", Name: "x", Value: "1"}},
	})
	if err == nil {
		t.Fatalf("Apply: want ErrVersionMismatch, got nil")
	}
	if !strings.Contains(err.Error(), "config_version mismatch") {
		t.Errorf("err = %v, want ErrVersionMismatch wrap", err)
	}
}

func TestApply_RoundTrip(t *testing.T) {
	db, _ := freshEngine(t)
	rows := []TestRow{
		{Kind: "a", Name: "x", Value: "1"},
		{Kind: "b", Name: "z", Value: "3"},
	}
	v, err := Apply(context.Background(), db, 0, false, map[string]any{
		"testrows": rows,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v != 1 {
		t.Errorf("version after apply = %d, want 1", v)
	}
	// re-apply with same data + new version → idempotent
	v2, err := Apply(context.Background(), db, 1, false, map[string]any{
		"testrows": rows,
	})
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if v2 != 2 {
		t.Errorf("version after 2nd apply = %d, want 2", v2)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&n)
	if n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
}

func TestApply_Force(t *testing.T) {
	db, _ := freshEngine(t)
	// version is 0; manifest claims 99; without force → error
	_, err := Apply(context.Background(), db, 99, false, map[string]any{})
	if err == nil {
		t.Fatal("want error without force")
	}
	// With force → bypass CAS
	v, err := Apply(context.Background(), db, 99, true, map[string]any{})
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if v != 1 {
		t.Errorf("version after forced apply = %d, want 1", v)
	}
}

// TestHooks_BeforeInsert exercises the write-side hook chain.
func TestHooks_BeforeInsert(t *testing.T) {
	reset()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	r := Register(Resource{
		Name:     "testrows",
		Table:    "testrows",
		RowType:  reflect.TypeOf(TestRow{}),
		PKFields: []string{"Kind", "Name"},
		Hooks: Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				p := row.(*TestRow)
				p.Value = "hooked:" + p.Value
				return nil
			},
		},
	})
	tx, _ := db.Begin()
	if err := r.Insert(context.Background(), tx, TestRow{Kind: "a", Name: "x", Value: "v"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	tx.Commit()
	var v string
	db.QueryRow(`SELECT value FROM testrows`).Scan(&v)
	if v != "hooked:v" {
		t.Errorf("hook did not run; value=%q", v)
	}
}

// TestColumnOverride_Write exercises the per-field write hook (e.g.
// nil-coalescing empty strings to NULL for nullable columns).
func TestColumnOverride_Write(t *testing.T) {
	reset()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE config_meta (version INTEGER NOT NULL DEFAULT 0);
		INSERT INTO config_meta VALUES (0);
		CREATE TABLE nullable (
		  name  TEXT PRIMARY KEY,
		  model TEXT  -- nullable
		);
	`); err != nil {
		t.Fatal(err)
	}
	type Row struct {
		Name  string `db:"name"`
		Model string `db:"model"`
	}
	r := Register(Resource{
		Name:     "nullable",
		Table:    "nullable",
		RowType:  reflect.TypeOf(Row{}),
		PKFields: []string{"Name"},
		Hooks: Hooks{
			ColumnOverride: map[string]ColumnHook{
				"Model": {
					Read: "COALESCE(model, '')",
					Write: func(v any) (any, error) {
						s := v.(string)
						if s == "" {
							return nil, nil
						}
						return s, nil
					},
				},
			},
		},
	})
	tx, _ := db.Begin()
	if err := r.Insert(context.Background(), tx, Row{Name: "a", Model: ""}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	tx.Commit()
	var isNull int
	db.QueryRow(`SELECT model IS NULL FROM nullable WHERE name='a'`).Scan(&isNull)
	if isNull != 1 {
		t.Errorf("empty Model should write NULL, got non-null")
	}
}

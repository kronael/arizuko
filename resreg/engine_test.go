package resreg

// Engine isolation tests — no arizuko-specific resource. Uses a synthetic
// `TestResource` struct + an in-memory SQLite to exercise scan/insert/
// delete/parse/emit/apply round-trips. Per spec 5/8 §"Testability".

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// TestRow is the synthetic schema. Composite PK on (kind, name); scope
// is "kind" so per-scope delete works. Mirrors the shape of acl_membership
// without depending on any arizuko code.
type TestRow struct {
	Kind  string `db:"kind"  yaml:"kind"`
	Name  string `db:"name"  yaml:"name"`
	Value string `db:"value" yaml:"value"`
}

// testSubsystem is the synthetic owner-DB tag these tests register
// "testrows" under — content-hash CAS (Apply/Export/Plan) is per subsystem.
const testSubsystem = "test"

// testSchema carries an audit_log table (id PK only) because Apply's write-
// lock trick issues one no-op UPDATE against it (spec 5/8 §"Content-hash
// CAS" — the real table exists in routd.db/onbod.db; resreg's own invoke()
// already assumes it for every mutation's audit row, so a synthetic test DB
// must too). No columns beyond `id` are needed here since every Apply call
// in this file passes opts=nil (no audit row insert, which needs the full
// column set — covered instead by resreg/resources' integration tests
// against the real schema).
const testSchema = `
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT);
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
		DB:       testSubsystem,
		RowType:  reflect.TypeFor[TestRow](),
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

// TestRowFilter_ExcludesFromScanDeleteAndChecksum: a resource's RowFilter
// keeps matching rows out of ScanAll, DeleteAll/DeleteScope, and therefore
// the content-hash checksum (Export/Checksum are built on ScanAll) — the
// same mechanism spec 5/8's CRITICAL finding uses to keep acl_membership
// pairing edges and route_tokens pairing tokens out of every
// manifest-visible read AND write.
func TestRowFilter_ExcludesFromScanDeleteAndChecksum(t *testing.T) {
	reset()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	r := Register(Resource{
		Name:      "testrows",
		Table:     "testrows",
		DB:        testSubsystem,
		RowType:   reflect.TypeFor[TestRow](),
		PKFields:  []string{"Kind", "Name"},
		Scope:     ScopeSpec{Field: "Kind"},
		RowFilter: "kind != 'excluded'",
	})
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "excluded", Name: "hidden", Value: "9"},
	)
	got, err := r.ScanAll(db)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if rows := got.([]TestRow); len(rows) != 1 || rows[0].Kind != "a" {
		t.Fatalf("ScanAll = %v, want only kind=a (excluded row must not appear)", rows)
	}
	c1, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	// A checksum computed after inserting MORE excluded-kind rows must be
	// unchanged — excluded rows never enter the projection Checksum hashes.
	insertRaw(t, db, TestRow{Kind: "excluded", Name: "hidden2", Value: "10"})
	c2, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum 2: %v", err)
	}
	if c1 != c2 {
		t.Errorf("checksum changed after inserting an excluded row: %s -> %s", c1, c2)
	}
	// DeleteAll must never touch excluded rows.
	tx, _ := db.Begin()
	if err := r.DeleteAll(context.Background(), tx); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	tx.Commit()
	var remaining int
	db.QueryRow(`SELECT COUNT(*) FROM testrows WHERE kind = 'excluded'`).Scan(&remaining)
	if remaining != 2 {
		t.Errorf("excluded rows remaining = %d, want 2 (DeleteAll must not touch them)", remaining)
	}
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&total)
	if total != 2 {
		t.Errorf("total rows = %d, want 2 (only the non-excluded row was deleted)", total)
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

// TestParseYAML_StrictRejectsUnknownKey: a typo'd top-level resource key
// rejects before the DB is touched (spec 5/8 §"Apply lifecycle" step 1).
func TestParseYAML_StrictRejectsUnknownKey(t *testing.T) {
	freshEngine(t)
	manifest := []byte(`
checksum: "sha256:0"
testrowz:
  - kind: a
    name: x
    value: "1"
`) // "testrowz" is a typo for "testrows"
	_, _, err := ParseYAML(manifest)
	if err == nil {
		t.Fatal("ParseYAML accepted a typo'd resource key")
	}
	if !strings.Contains(err.Error(), "unknown resource key") || !strings.Contains(err.Error(), "testrowz") {
		t.Errorf("err = %v, want unknown-resource-key naming testrowz", err)
	}
}

// TestParseYAML_StrictRejectsUnknownField: a bogus row field rejects, so
// an operator's misspelled column can't silently drop (spec 5/8 step 1).
func TestParseYAML_StrictRejectsUnknownField(t *testing.T) {
	freshEngine(t)
	manifest := []byte(`
checksum: "sha256:0"
testrows:
  - kind: a
    name: x
    value: "1"
    bogus_field: oops
`)
	_, _, err := ParseYAML(manifest)
	if err == nil {
		t.Fatal("ParseYAML accepted a bogus row field")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("err = %v, want bogus_field named", err)
	}
}

// TestParseYAML_ChecksumNotInResourceMap: "checksum" is extracted
// separately and never left as a manifestRows key — Apply's per-key
// subsystem validation would otherwise try (and fail) to look it up as a
// resource name.
func TestParseYAML_ChecksumNotInResourceMap(t *testing.T) {
	freshEngine(t)
	manifest := []byte(`
checksum: "sha256:deadbeef"
testrows: []
`)
	rows, checksum, err := ParseYAML(manifest)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if checksum != "sha256:deadbeef" {
		t.Errorf("checksum = %q, want sha256:deadbeef", checksum)
	}
	if _, ok := rows["checksum"]; ok {
		t.Error("checksum must not be a manifestRows key")
	}
}

func TestYAMLEmit_Deterministic(t *testing.T) {
	db, _ := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "b", Name: "z", Value: "3"},
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
	)
	m1, err := Export(db, testSubsystem)
	if err != nil {
		t.Fatalf("Export 1: %v", err)
	}
	b1, err := EmitYAML(m1)
	if err != nil {
		t.Fatalf("Emit 1: %v", err)
	}
	m2, err := Export(db, testSubsystem)
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

func TestApply_ChecksumMismatch(t *testing.T) {
	db, _ := freshEngine(t)
	// db's testrows table is empty; a bogus checksum can never match it.
	_, err := Apply(context.Background(), db, testSubsystem, "sha256:bogus", false, map[string]any{
		"testrows": []TestRow{{Kind: "a", Name: "x", Value: "1"}},
	}, nil)
	if err == nil {
		t.Fatalf("Apply: want ErrChecksumMismatch, got nil")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// TestApply_RoundTrip: applying the same content twice is a no-op on the
// checksum (content-hash CAS's headline property over the old counter,
// which advanced even on an idempotent re-apply of identical rows).
func TestApply_RoundTrip(t *testing.T) {
	db, _ := freshEngine(t)
	rows := []TestRow{
		{Kind: "a", Name: "x", Value: "1"},
		{Kind: "b", Name: "z", Value: "3"},
	}
	c0, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	c1, err := Apply(context.Background(), db, testSubsystem, c0, false, map[string]any{
		"testrows": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if c1 == c0 {
		t.Errorf("checksum unchanged after apply that added rows")
	}
	// re-apply the SAME content, CAS'd against the checksum Apply just
	// returned → idempotent, checksum unchanged.
	c2, err := Apply(context.Background(), db, testSubsystem, c1, false, map[string]any{
		"testrows": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if c2 != c1 {
		t.Errorf("checksum changed on idempotent re-apply of identical content: %s -> %s", c1, c2)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&n)
	if n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
}

// TestExportApply_FullRoundTrip is the import/export contract end to end:
// export a configured DB, apply that manifest to a fresh DB, re-export, and
// the config data must be identical. This is the "agent is data" backup/restore
// guarantee — `arizuko export` → git → `arizuko apply` reproduces the config
// exactly.
func TestExportApply_FullRoundTrip(t *testing.T) {
	db1, _ := freshEngine(t)
	insertRaw(t, db1,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	m1, err := Export(db1, testSubsystem)
	if err != nil {
		t.Fatalf("Export db1: %v", err)
	}

	db2, _ := freshEngine(t) // fresh empty DB; registry re-registers testrows
	c0, err := Checksum(db2, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum db2: %v", err)
	}
	if _, err := Apply(context.Background(), db2, testSubsystem, c0, false, map[string]any{
		"testrows": m1["testrows"],
	}, nil); err != nil {
		t.Fatalf("Apply exported manifest to a fresh DB: %v", err)
	}
	m2, err := Export(db2, testSubsystem)
	if err != nil {
		t.Fatalf("Export db2: %v", err)
	}

	y1, _ := yaml.Marshal(m1)
	y2, _ := yaml.Marshal(m2)
	if string(y1) != string(y2) {
		t.Errorf("export→apply→export not stable:\n--- exported ---\n%s\n--- re-exported ---\n%s", y1, y2)
	}
}

// TestApply_EmptyScopedListClears: a manifest mentioning a scoped resource
// with an EMPTY list wipes all its rows — the declarative way to remove a
// scoped resource entirely. A resource ABSENT from the manifest is left
// untouched (Bug 7 — an empty list produced zero scopes, so DeleteScope
// never ran and stale rows survived; absent must still be a no-op).
func TestApply_EmptyScopedListClears(t *testing.T) {
	db, _ := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)

	// Manifest MENTIONS testrows with an empty list → clears every row.
	c0, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(context.Background(), db, testSubsystem, c0, false, map[string]any{
		"testrows": []TestRow{},
	}, nil); err != nil {
		t.Fatalf("Apply empty: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows after empty-list apply = %d, want 0 (empty list clears)", n)
	}

	// Re-seed, then apply a manifest that does NOT mention testrows at all →
	// the resource is untouched. A direct-SQL insert (bypassing Apply)
	// changes the DB's content hash, so the CAS token must be recomputed
	// fresh — exactly the class of drift a counter-based CAS would have
	// missed (spec 5/8 §"Content-hash CAS": "it catches a manual sqlite3
	// edit").
	insertRaw(t, db, TestRow{Kind: "a", Name: "x", Value: "1"})
	c1, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum 2: %v", err)
	}
	if _, err := Apply(context.Background(), db, testSubsystem, c1, false, map[string]any{}, nil); err != nil {
		t.Fatalf("Apply absent: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM testrows`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows after absent-from-manifest apply = %d, want 1 (untouched)", n)
	}
}

// TestPlan_EmptyScopedListMatchesApply: plan for an empty-list scoped
// resource must report every live row as a Remove — plan and apply must
// agree (Bug 7: scopeFilter used to return an empty set, hiding the wipe).
func TestPlan_EmptyScopedListMatchesApply(t *testing.T) {
	db, r := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	d, err := r.Diff(db, []TestRow{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Remove) != 2 {
		t.Fatalf("Remove = %v, want both live rows (empty list wipes all)", d.Remove)
	}
	if !d.Changed() {
		t.Error("Changed() = false, want true (a wipe is a change)")
	}
}

func TestApply_Force(t *testing.T) {
	db, _ := freshEngine(t)
	// testrows is empty; manifest claims a bogus checksum → without force, error.
	_, err := Apply(context.Background(), db, testSubsystem, "sha256:bogus", false, map[string]any{}, nil)
	if err == nil {
		t.Fatal("want error without force")
	}
	// With force → bypass CAS.
	sum, err := Apply(context.Background(), db, testSubsystem, "sha256:bogus", true, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if sum == "" {
		t.Errorf("checksum after forced apply is empty")
	}
}

// TestApply_UnknownSubsystem: a manifestRows key naming a resource from a
// DIFFERENT subsystem than the one Apply was called against rejects before
// touching the DB — spec 5/8 §"Document schema": a document meant for one
// owner DB must not carry another subsystem's rows.
func TestApply_UnknownSubsystem(t *testing.T) {
	freshEngine(t)
	reset()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	Register(Resource{
		Name:     "testrows",
		Table:    "testrows",
		DB:       "other-subsystem",
		RowType:  reflect.TypeFor[TestRow](),
		PKFields: []string{"Kind", "Name"},
		Scope:    ScopeSpec{Field: "Kind"},
	})
	_, err = Apply(context.Background(), db, testSubsystem, "", true, map[string]any{
		"testrows": []TestRow{{Kind: "a", Name: "x", Value: "1"}},
	}, nil)
	if err == nil {
		t.Fatal("want error: testrows belongs to other-subsystem, not test")
	}
	if !strings.Contains(err.Error(), "other-subsystem") {
		t.Errorf("err = %v, want it to name the resource's real subsystem", err)
	}
}

// TestDiff_AddUpdateRemove exercises the non-mutating plan diff: a row
// only in the manifest is an add, a row only in the DB is a remove, a
// PK in both with a differing payload is an update, identical is
// unchanged. Per spec 5/8 §"Apply lifecycle" step 3.
func TestDiff_AddUpdateRemove(t *testing.T) {
	db, r := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"}, // update target (payload differs below)
		TestRow{Kind: "a", Name: "y", Value: "2"}, // unchanged
		TestRow{Kind: "a", Name: "z", Value: "3"}, // remove (absent from manifest, scope "a" is mentioned)
	)
	manifest := []TestRow{
		{Kind: "a", Name: "x", Value: "CHANGED"}, // same PK (a,x), new value → update
		{Kind: "a", Name: "y", Value: "2"},       // identical → unchanged
		{Kind: "c", Name: "new", Value: "9"},     // absent from DB → add
	}
	d, err := r.Diff(db, manifest)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Add) != 1 || !strings.Contains(d.Add[0], "c|new|") {
		t.Errorf("Add = %v, want one (c,new)", d.Add)
	}
	if len(d.Update) != 1 || !strings.Contains(d.Update[0], "a|x|") {
		t.Errorf("Update = %v, want one (a,x)", d.Update)
	}
	if len(d.Unchanged) != 1 {
		t.Errorf("Unchanged = %v, want one", d.Unchanged)
	}
	if len(d.Remove) != 1 || !strings.Contains(d.Remove[0], "a|z|") {
		t.Errorf("Remove = %v, want one (a,z)", d.Remove)
	}
	if !d.Changed() {
		t.Error("Changed() = false, want true")
	}
}

// TestDiff_ScopedRemoveLeavesOutOfScope: for a scoped resource, a live
// row outside the folders the manifest mentions is NOT reported for
// Remove — apply leaves it alone, so plan must too (spec 5/8 §"Surface").
// TestRow's scope is "kind"; a manifest touching only kind "a" leaves a
// kind "b" row untouched.
func TestDiff_ScopedRemoveLeavesOutOfScope(t *testing.T) {
	db, r := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"}, // in-scope, absent from manifest → remove
		TestRow{Kind: "b", Name: "z", Value: "3"}, // out-of-scope → must NOT remove
	)
	manifest := []TestRow{
		{Kind: "a", Name: "keep", Value: "9"}, // mentions only scope "a"
	}
	d, err := r.Diff(db, manifest)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Remove) != 1 || !strings.Contains(d.Remove[0], "a|x|") {
		t.Errorf("Remove = %v, want only (a,x) — (b,z) is out of scope", d.Remove)
	}
}

// TestPlan_NoChangeAfterApply: applying a manifest then planning the
// same manifest reports zero changes (idempotent plan, the no-op
// acceptance criterion's non-mutating half).
func TestPlan_NoChangeAfterApply(t *testing.T) {
	db, _ := freshEngine(t)
	manifest := map[string]any{
		"testrows": []TestRow{
			{Kind: "a", Name: "x", Value: "1"},
			{Kind: "b", Name: "z", Value: "3"},
		},
	}
	c0, err := Checksum(db, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(context.Background(), db, testSubsystem, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deltas, err := Plan(db, testSubsystem, manifest)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, d := range deltas {
		if d.Changed() {
			t.Errorf("%s changed after applying same manifest: %+v", d.Resource, d)
		}
	}
}

// TestGetResource_RoundTrip: GetResource emits a fragment whose parsed
// rows equal the live rows, so re-applying it is a no-op. Per spec 5/8
// §"arizuko get round-trip".
func TestGetResource_RoundTrip(t *testing.T) {
	db, _ := freshEngine(t)
	insertRaw(t, db,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	frag, err := GetResource(db, "testrows")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	if _, ok := frag["testrows"]; !ok {
		t.Fatalf("fragment missing testrows key: %v", frag)
	}
	if _, ok := frag["checksum"]; ok {
		t.Error("fragment must not carry a checksum")
	}
	out, err := EmitYAML(frag)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	parsed, _, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	r := Lookup("testrows")
	d, err := r.Diff(db, parsed["testrows"])
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Changed() {
		t.Errorf("get fragment is not a no-op on re-apply: %+v", d)
	}
	if _, err := GetResource(db, "nope"); err == nil {
		t.Error("GetResource(unknown) should error")
	}
}

// TestRetarget_RewritesScopeFieldOnly: Retarget rewrites the declared
// Scope.Field column and nothing else, on a COPY (the input slice is
// untouched) — spec 5/8 §"Path-retargeting apply".
func TestRetarget_RewritesScopeFieldOnly(t *testing.T) {
	_, r := freshEngine(t)
	rows := []TestRow{
		{Kind: "a", Name: "x", Value: "1"},
		{Kind: "a", Name: "y", Value: "2"},
	}
	got, err := r.Retarget(rows, "b")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	out := got.([]TestRow)
	if len(out) != 2 || out[0].Kind != "b" || out[1].Kind != "b" {
		t.Fatalf("Retarget = %v, want every row's Kind rewritten to b", out)
	}
	if out[0].Name != "x" || out[1].Name != "y" || out[0].Value != "1" || out[1].Value != "2" {
		t.Errorf("Retarget touched non-scope fields: %v", out)
	}
	if rows[0].Kind != "a" || rows[1].Kind != "a" {
		t.Errorf("Retarget mutated the input slice in place: %v", rows)
	}
}

// TestRetarget_RefusesUnscopedResource: a resource with no declared Scope
// (routes/acl/scheduled_tasks/secrets in the real registry) errors rather
// than guessing which column is the folder.
func TestRetarget_RefusesUnscopedResource(t *testing.T) {
	reset()
	r := Register(Resource{
		Name:     "testrows",
		Table:    "testrows",
		DB:       testSubsystem,
		RowType:  reflect.TypeFor[TestRow](),
		PKFields: []string{"Kind", "Name"},
		// No Scope set.
	})
	_, err := r.Retarget([]TestRow{{Kind: "a", Name: "x", Value: "1"}}, "b")
	if err == nil {
		t.Fatal("want error: resource has no ScopeSpec")
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
		DB:       testSubsystem,
		RowType:  reflect.TypeFor[TestRow](),
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
		DB:       testSubsystem,
		RowType:  reflect.TypeFor[Row](),
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

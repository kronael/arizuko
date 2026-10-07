package resreg

// Engine isolation tests — no arizuko-specific resource. A synthetic `TestRow`
// struct over an in-memory database exercises the manifest lifecycle: scan,
// parse, emit, plan, diff, retarget, and the content-hash CAS an apply turns
// on.
//
// The fixture is ENTITY-shaped, because that is the shape Apply and
// ExportSnapshot serve (requireEntities, entity.go) and the shape every
// registered resource declares (resreg/resources/shape_test.go). What the
// object path does with a row — the coordinate, the codec, the scoped delete
// inside the claim — is entity_test.go's; what is here is the lifecycle around
// it.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// TestRow is the synthetic schema. The coordinate is (kind, name) and the scope
// is "kind", its leading field, so a scoped apply reaches one kind's items the
// way a folder purge reaches one folder's. Mirrors the shape of acl_membership
// without depending on any arizuko code.
type TestRow struct {
	Kind  string `db:"kind"  yaml:"kind"`
	Name  string `db:"name"  yaml:"name"`
	Value string `db:"value" yaml:"value"`
}

// testSubsystem is the synthetic owner-DB tag these tests register "testrows"
// under. Export, Checksum, Plan and Apply are per subsystem, and the subsystem
// is also the partition every item of the resource lives in.
const testSubsystem = "test"

// testKeySep joins the two coordinate fields. No kind and no name below carries
// it, which is what the declaration requires of every resource
// (Resource.EntityKeySep).
const testKeySep = "\x1f"

// freshEngine resets the package registry, registers TestRow as an
// entity-backed resource, and returns a database prepared to serve it.
//
// The preparation goes through EnsureSubsystem — the call a daemon's opener
// makes — so a missing entity or a missing version marker fails here the way it
// would fail at boot.
func freshEngine(t *testing.T) (Backend, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "testrows",
		Table:         "testrows",
		DB:            testSubsystem,
		RowType:       reflect.TypeFor[TestRow](),
		PKFields:      []string{"Kind", "Name"},
		EntitySortKey: []string{"Kind", "Name"},
		EntityKeySep:  testKeySep,
		Scope:         ScopeSpec{Field: "Kind"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, testSubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), r
}

// insertRaw writes items straight through the driver, deliberately bypassing
// the engine under test.
func insertRaw(t *testing.T, be Backend, rows ...TestRow) {
	t.Helper()
	for _, row := range rows {
		committed, err := be.Obj.GuardedBatch(t.Context(), nil, []objstore.Write{{
			Op: objstore.Put, Entity: "testrows",
			Key: objstore.Key{
				Partition: testSubsystem,
				Sort:      row.Kind + testKeySep + row.Name,
			},
			Attrs: map[string]string{"value": row.Value},
		}})
		if err != nil || !committed {
			t.Fatalf("insert raw %s/%s: committed=%v err=%v", row.Kind, row.Name, committed, err)
		}
	}
}

// liveRows reads a resource's rows back through the engine. A count taken past
// the engine would have to restate the item layout here, and the layout is what
// the engine owns.
func liveRows(t *testing.T, be Backend, r *Resource) []TestRow {
	t.Helper()
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	return got.([]TestRow)
}

func TestScanAll_RoundTrip(t *testing.T) {
	be, r := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	got, err := r.ScanAll(be)
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

// TestRowFilter_ExcludesFromScanAndChecksum: a resource's RowFilter keeps
// matching rows out of ScanAll and therefore out of the content-hash checksum
// (Export and Checksum are built on ScanAll) — the same mechanism spec 5/8's
// CRITICAL finding uses to keep acl_membership pairing edges and route_tokens
// pairing tokens out of every manifest-visible read. What the same declaration
// does to the two deletes is TestEntity_RowFilterNarrowsTheWipe and
// TestEntity_RowFilterNarrowsTheWipeAndTheScopedDelete.
//
// The filter compares `value` because the other two fields are the coordinate,
// and a coordinate is a key rather than an attribute a Where can reach
// (initEntity).
func TestRowFilter_ExcludesFromScanAndChecksum(t *testing.T) {
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "testrows",
		Table:         "testrows",
		DB:            testSubsystem,
		RowType:       reflect.TypeFor[TestRow](),
		PKFields:      []string{"Kind", "Name"},
		EntitySortKey: []string{"Kind", "Name"},
		EntityKeySep:  testKeySep,
		Scope:         ScopeSpec{Field: "Kind"},
		RowFilter:     []objstore.Cmp{{Attr: "value", Op: objstore.Ne, Value: "excluded"}},
	})
	if err := EnsureSubsystem(t.Context(), s, s, testSubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	be := On(s)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "hidden", Value: "excluded"},
	)
	if rows := liveRows(t, be, r); len(rows) != 1 || rows[0].Name != "x" {
		t.Fatalf("ScanAll = %v, want only the unfiltered row", rows)
	}
	c1, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	// A checksum computed after writing MORE excluded rows must be unchanged —
	// excluded rows never enter the projection Checksum hashes.
	insertRaw(t, be, TestRow{Kind: "a", Name: "hidden2", Value: "excluded"})
	c2, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum 2: %v", err)
	}
	if c1 != c2 {
		t.Errorf("checksum changed after writing an excluded row: %s -> %s", c1, c2)
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
// rejects before the DB is touched.
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

// A package's `env` section rides the same document as its rows: one parse
// yields both, and the section never leaks into the resource map, where
// SubsystemOf would fail looking it up.
func TestParseManifest_EnvSectionBesideRows(t *testing.T) {
	freshEngine(t)
	manifest := []byte(`
env:
  GW_KEY: {generate: true}
  GW_URL: {default: "http://gw:4000"}
  GW_TOKEN: {required: true}
testrows:
  - kind: a
    name: x
    value: "1"
`)
	rows, env, _, err := ParseManifest(manifest)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if _, ok := rows[PackageEnvKey]; ok {
		t.Error("env must not be a resource key")
	}
	if len(rows["testrows"].([]TestRow)) != 1 {
		t.Errorf("rows lost beside env: %v", rows)
	}
	want := map[string]EnvNeed{
		"GW_KEY":   {Generate: true},
		"GW_URL":   {Default: "http://gw:4000"},
		"GW_TOKEN": {Required: true},
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %+v, want %+v", env, want)
	}
}

// An entry with no source, two sources, a misspelled source or a non-env
// key refuses, so a package that needs a value nothing supplies never
// installs; and ParseYAML — the row-only reader `apply` uses — refuses the
// section outright rather than applying the rows and dropping the env.
func TestParseManifest_EnvStrict(t *testing.T) {
	freshEngine(t)
	for name, body := range map[string]string{
		"no source":   "env:\n  GW_KEY: {}\n",
		"two sources": "env:\n  GW_KEY: {generate: true, default: x}\n",
		"typo":        "env:\n  GW_KEY: {defualt: x}\n",
		"bad key":     "env:\n  gw-key: {generate: true}\n",
		"not mapping": "env:\n  - GW_KEY\n",
	} {
		if _, _, _, err := ParseManifest([]byte(body)); err == nil {
			t.Errorf("%s: ParseManifest accepted %q", name, body)
		}
	}
	if _, _, err := ParseYAML([]byte("env:\n  GW_KEY: {generate: true}\ntestrows: []\n")); err == nil {
		t.Error("ParseYAML must refuse a package env section")
	}
}

func TestYAMLEmit_Deterministic(t *testing.T) {
	be, _ := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "b", Name: "z", Value: "3"},
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
	)
	m1, err := Export(be, testSubsystem)
	if err != nil {
		t.Fatalf("Export 1: %v", err)
	}
	b1, err := EmitYAML(m1)
	if err != nil {
		t.Fatalf("Emit 1: %v", err)
	}
	m2, err := Export(be, testSubsystem)
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
	if ix < 0 || iy <= ix || iz <= iy {
		t.Errorf("rows not PK-sorted in emit:\n%s", out)
	}
}

func TestApply_ChecksumMismatch(t *testing.T) {
	be, _ := freshEngine(t)
	// db's testrows table is empty; a bogus checksum can never match it.
	_, err := Apply(context.Background(), be, testSubsystem, "sha256:bogus", false, map[string]any{
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
	be, r := freshEngine(t)
	rows := []TestRow{
		{Kind: "a", Name: "x", Value: "1"},
		{Kind: "b", Name: "z", Value: "3"},
	}
	c0, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	c1, err := Apply(context.Background(), be, testSubsystem, c0, false, map[string]any{
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
	c2, err := Apply(context.Background(), be, testSubsystem, c1, false, map[string]any{
		"testrows": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if c2 != c1 {
		t.Errorf("checksum changed on idempotent re-apply of identical content: %s -> %s", c1, c2)
	}
	if live := liveRows(t, be, r); len(live) != 2 {
		t.Errorf("row count = %d, want 2", len(live))
	}
}

// TestExportApply_FullRoundTrip is the import/export contract end to end:
// export a configured DB, apply that manifest to a fresh DB, re-export, and
// the config data must be identical. This is the "agent is data" backup/restore
// guarantee — `arizuko export` → git → `arizuko apply` reproduces the config
// exactly.
func TestExportApply_FullRoundTrip(t *testing.T) {
	be1, _ := freshEngine(t)
	insertRaw(t, be1,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "a", Name: "y", Value: "2"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	m1, err := Export(be1, testSubsystem)
	if err != nil {
		t.Fatalf("Export db1: %v", err)
	}

	be2, _ := freshEngine(t) // fresh empty DB; registry re-registers testrows
	c0, err := Checksum(be2, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum db2: %v", err)
	}
	if _, err := Apply(context.Background(), be2, testSubsystem, c0, false, map[string]any{
		"testrows": m1["testrows"],
	}, nil); err != nil {
		t.Fatalf("Apply exported manifest to a fresh DB: %v", err)
	}
	m2, err := Export(be2, testSubsystem)
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
	be, r := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)

	// Manifest MENTIONS testrows with an empty list → clears every row.
	c0, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(context.Background(), be, testSubsystem, c0, false, map[string]any{
		"testrows": []TestRow{},
	}, nil); err != nil {
		t.Fatalf("Apply empty: %v", err)
	}
	if live := liveRows(t, be, r); len(live) != 0 {
		t.Fatalf("rows after empty-list apply = %d, want 0 (empty list clears)", len(live))
	}

	// Re-seed, then apply a manifest that does NOT mention testrows at all →
	// the resource is untouched. A direct-SQL insert (bypassing Apply)
	// changes the DB's content hash, so the CAS token must be recomputed
	// fresh — exactly the class of drift a counter-based CAS would have
	// missed (spec 5/8 §"Content-hash CAS": "it catches a manual sqlite3
	// edit").
	insertRaw(t, be, TestRow{Kind: "a", Name: "x", Value: "1"})
	c1, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum 2: %v", err)
	}
	if _, err := Apply(context.Background(), be, testSubsystem, c1, false, map[string]any{}, nil); err != nil {
		t.Fatalf("Apply absent: %v", err)
	}
	if live := liveRows(t, be, r); len(live) != 1 {
		t.Fatalf("rows after absent-from-manifest apply = %d, want 1 (untouched)", len(live))
	}
}

// TestPlan_EmptyScopedListMatchesApply: plan for an empty-list scoped
// resource must report every live row as a Remove — plan and apply must
// agree (Bug 7: scopeFilter used to return an empty set, hiding the wipe).
func TestPlan_EmptyScopedListMatchesApply(t *testing.T) {
	be, r := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	d, err := r.Diff(be, []TestRow{})
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
	be, _ := freshEngine(t)
	// testrows is empty; manifest claims a bogus checksum → without force, error.
	_, err := Apply(context.Background(), be, testSubsystem, "sha256:bogus", false, map[string]any{}, nil)
	if err == nil {
		t.Fatal("want error without force")
	}
	// With force → bypass CAS.
	sum, err := Apply(context.Background(), be, testSubsystem, "sha256:bogus", true, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Apply --force: %v", err)
	}
	if sum == "" {
		t.Errorf("checksum after forced apply is empty")
	}
}

// TestApply_UnknownSubsystem: a manifestRows key naming a resource from a
// DIFFERENT subsystem than the one Apply was called against rejects before
// touching the DB — spec 5/8 §"Surface": a document meant for one owner DB
// must not carry another subsystem's rows.
func TestApply_UnknownSubsystem(t *testing.T) {
	be, _ := freshEngine(t)
	reset()
	Register(Resource{
		Name:          "testrows",
		Table:         "testrows",
		DB:            "other-subsystem",
		RowType:       reflect.TypeFor[TestRow](),
		PKFields:      []string{"Kind", "Name"},
		EntitySortKey: []string{"Kind", "Name"},
		EntityKeySep:  testKeySep,
		Scope:         ScopeSpec{Field: "Kind"},
	})
	_, err := Apply(context.Background(), be, testSubsystem, "", true, map[string]any{
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
// unchanged. Per spec 5/8 §"Surface".
func TestDiff_AddUpdateRemove(t *testing.T) {
	be, r := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"}, // update target (payload differs below)
		TestRow{Kind: "a", Name: "y", Value: "2"}, // unchanged
		TestRow{Kind: "a", Name: "z", Value: "3"}, // remove (absent from manifest, scope "a" is mentioned)
	)
	manifest := []TestRow{
		{Kind: "a", Name: "x", Value: "CHANGED"}, // same PK (a,x), new value → update
		{Kind: "a", Name: "y", Value: "2"},       // identical → unchanged
		{Kind: "c", Name: "new", Value: "9"},     // absent from DB → add
	}
	d, err := r.Diff(be, manifest)
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
	be, r := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"}, // in-scope, absent from manifest → remove
		TestRow{Kind: "b", Name: "z", Value: "3"}, // out-of-scope → must NOT remove
	)
	manifest := []TestRow{
		{Kind: "a", Name: "keep", Value: "9"}, // mentions only scope "a"
	}
	d, err := r.Diff(be, manifest)
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
	be, _ := freshEngine(t)
	manifest := map[string]any{
		"testrows": []TestRow{
			{Kind: "a", Name: "x", Value: "1"},
			{Kind: "b", Name: "z", Value: "3"},
		},
	}
	c0, err := Checksum(be, testSubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(context.Background(), be, testSubsystem, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deltas, err := Plan(be, testSubsystem, manifest)
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
// §"Round-trip honesty".
func TestGetResource_RoundTrip(t *testing.T) {
	be, _ := freshEngine(t)
	insertRaw(t, be,
		TestRow{Kind: "a", Name: "x", Value: "1"},
		TestRow{Kind: "b", Name: "z", Value: "3"},
	)
	frag, err := GetResource(be, "testrows")
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
	d, err := r.Diff(be, parsed["testrows"])
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Changed() {
		t.Errorf("get fragment is not a no-op on re-apply: %+v", d)
	}
	if _, err := GetResource(be, "nope"); err == nil {
		t.Error("GetResource(OnDB(unknown)) should error")
	}
}

// TestRetarget_RewritesScopeFieldOnly: Retarget rewrites the declared
// Scope.Field column and nothing else, on a COPY (the input slice is
// untouched) — spec 5/8 §"Path retargeting: primitive shipped, no caller".
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
		Name:          "testrows",
		Table:         "testrows",
		DB:            testSubsystem,
		RowType:       reflect.TypeFor[TestRow](),
		PKFields:      []string{"Kind", "Name"},
		EntitySortKey: []string{"Kind", "Name"},
		EntityKeySep:  testKeySep,
		// No Scope set.
	})
	_, err := r.Retarget([]TestRow{{Kind: "a", Name: "x", Value: "1"}}, "b")
	if err == nil {
		t.Fatal("want error: resource has no ScopeSpec")
	}
}

// BUGS Z2: an `omitempty` slice with no value is omitted from the export, so it
// re-parses as nil, while the live row's AfterScan builds an empty slice from
// the stored "[]". reflect.DeepEqual calls those different, so every such row
// reported a phantom `update` and `plan` on an unmodified export lied — 12 of
// 12 proxyd_routes rows on krons. The engine normalizes, so the fix covers
// every resource rather than the one that reported it.
func TestEqualIgnoringEmpty_NilAndEmptyAreTheSame(t *testing.T) {
	type row struct {
		Name    string              `db:"name"`
		Headers []string            `db:"-"`
		Tags    map[string][]string `db:"-"`
	}
	// Go through payloadEqual, the Diff call site — comparing the helper
	// directly would still pass if payloadEqual reverted to reflect.DeepEqual.
	res := &Resource{Name: "z2rows", Table: "z2rows", RowType: reflect.TypeFor[row](), PKFields: []string{"Name"}}
	res.initMeta()
	eq := func(a, b row) bool {
		return res.payloadEqual(reflect.ValueOf(a), reflect.ValueOf(b))
	}

	if !eq(row{Name: "x"}, row{Name: "x", Headers: []string{}}) {
		t.Error("a nil slice and an empty slice must compare equal")
	}
	if !eq(row{Name: "x"}, row{Name: "x", Tags: map[string][]string{}}) {
		t.Error("a nil map and an empty map must compare equal")
	}
	// The normalization must not swallow a real difference.
	if eq(row{Name: "x"}, row{Name: "x", Headers: []string{"authorization"}}) {
		t.Error("a populated slice must differ from an absent one")
	}
	if eq(row{Name: "x"}, row{Name: "y"}) {
		t.Error("a differing scalar must still differ")
	}
	if eq(row{Headers: []string{"a"}}, row{Headers: []string{"b"}}) {
		t.Error("differing slice contents must differ")
	}
	// An empty inner slice inside a map is the same case, one level down.
	if !eq(row{Tags: map[string][]string{"k": nil}}, row{Tags: map[string][]string{"k": {}}}) {
		t.Error("a nil and an empty slice inside a map must compare equal")
	}
	if eq(row{Tags: map[string][]string{"k": {"a"}}}, row{Tags: map[string][]string{"j": {"a"}}}) {
		t.Error("differing map keys must differ")
	}
}

// TestMount_CarriesCatalogShape is the collapse's own control: Mount hands back
// the registered shape whole, so a mount site has nothing left to restate. Every
// field checked here was previously copied by hand at each of ~20 mount sites.
func TestMount_CarriesCatalogShape(t *testing.T) {
	Register(Resource{
		Name:  "mountprobe",
		Table: "mountprobe",
		RowType: reflect.TypeFor[struct {
			Key string `db:"key" json:"key"`
		}](),
		PKFields:  []string{"Key"},
		Endpoints: []Endpoint{{Verb: "GET", Path: "/v1/mountprobe", Action: ActionList}},
		MCPDoc:    map[Action]string{ActionList: "list them"},
		MCPArgs:   map[Action][]MCPArg{ActionList: {{Name: "q", Type: "string"}}},
		MCPNames:  map[Action]string{ActionList: "list_mountprobes"},
	})
	reg := Lookup("mountprobe")
	got := Mount("mountprobe")

	if got.Name != reg.Name || got.Table != reg.Table || got.RowType != reg.RowType {
		t.Errorf("Mount dropped identity/schema: %+v", got)
	}
	if !reflect.DeepEqual(got.Endpoints, reg.Endpoints) {
		t.Errorf("Mount Endpoints = %v, want %v", got.Endpoints, reg.Endpoints)
	}
	if !reflect.DeepEqual(got.MCPDoc, reg.MCPDoc) ||
		!reflect.DeepEqual(got.MCPArgs, reg.MCPArgs) ||
		!reflect.DeepEqual(got.MCPNames, reg.MCPNames) {
		t.Error("Mount dropped MCP metadata — the agent's tool names, args or descriptions would differ from the catalog")
	}
	// Behavior is the mount site's half and must arrive empty, or a daemon could
	// inherit another surface's gate.
	if got.Handler != nil || got.Gate != nil || got.Authz != nil || got.Store != nil {
		t.Error("Mount carried behavior fields — those belong to the mounting daemon")
	}
}

// TestMount_PanicsOnUnregistered: a daemon mounting a name no catalog knows
// serves a path that /openapi.json and the CLI manifest cannot see. That must
// stop the binary at boot rather than surface later as a missing row.
func TestMount_PanicsOnUnregistered(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Mount of an unregistered name returned instead of panicking")
		}
	}()
	_ = Mount("no_such_resource")
}

package resreg

// The object path, in isolation — a synthetic entity-backed resource, no
// arizuko-specific declaration. The column path's twin bodies are in
// engine_test.go, and where a body here asserts the SAME answer as one there
// (the checksum, the apply round-trip) it says so, because "both shapes agree"
// is the claim the dispatch rests on.

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// EntityRow is the synthetic entity-backed schema: a unique string coordinate,
// one OPTIONAL string attribute and one int. Those three cover what the object
// path renders — the sort key that leaves the attribute set, the empty string
// that becomes an absent attribute, and the number that is stored as text.
type EntityRow struct {
	Key   string `db:"key"   yaml:"key"`
	Note  string `db:"note"  yaml:"note,omitempty"`
	Count int    `db:"count" yaml:"count"`
}

// entitySubsystem is the synthetic owner-DB tag, and it is also the PARTITION
// every item of this resource lives in (Resource.EntitySortKey).
const entitySubsystem = "entitytest"

// freshEntity resets the registry, registers an entity-backed resource, and
// returns a database prepared to serve it.
//
// The preparation goes through EnsureSubsystem — the call a daemon's opener
// makes — rather than a CREATE TABLE beside it, so a missing entity or a missing
// version marker fails here the way it would fail at boot.
func freshEntity(t *testing.T) (*sql.DB, objstore.Store, *Resource) {
	t.Helper()
	reset()
	db := dbtest.Open(t)
	s := sqlstore.New(db)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "entityrows",
		Table:         "entityrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[EntityRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return db, s, r
}

// putItem writes one item straight through the driver, deliberately bypassing
// the engine under test.
func putItem(t *testing.T, s objstore.Store, sort string, attrs map[string]string) {
	t.Helper()
	committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: "entityrows",
		Key:   objstore.Key{Partition: entitySubsystem, Sort: sort},
		Attrs: attrs,
	}})
	if err != nil || !committed {
		t.Fatalf("put %s: committed=%v err=%v", sort, committed, err)
	}
}

func TestEntity_ScanAllReadsItems(t *testing.T) {
	db, s, r := freshEntity(t)
	putItem(t, s, "b", map[string]string{"note": "second", "count": "2"})
	putItem(t, s, "a", map[string]string{"note": "first", "count": "1"})

	got, err := r.ScanAll(Backend{DB: db, Obj: s})
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows, ok := got.([]EntityRow)
	if !ok {
		t.Fatalf("ScanAll returned %T, want []EntityRow", got)
	}
	want := []EntityRow{{Key: "a", Note: "first", Count: 1}, {Key: "b", Note: "second", Count: 2}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v (sort-key order, key rebuilt from the coordinate)", rows, want)
	}
}

// The sort key is NOT stored as an attribute, so an item carrying one under the
// same name is still read from its coordinate. Storing the coordinate twice is
// two facts that can disagree, and this pins which one wins.
func TestEntity_SortKeyComesFromTheCoordinate(t *testing.T) {
	db, s, r := freshEntity(t)
	putItem(t, s, "real", map[string]string{"key": "impostor", "count": "0"})

	got, err := r.ScanAll(Backend{DB: db, Obj: s})
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]EntityRow)
	if rows[0].Key != "real" {
		t.Errorf("Key = %q, want %q", rows[0].Key, "real")
	}
}

// An absent attribute IS the empty string, in both directions. That is what
// deletes the COALESCE-on-read / nil-on-write pair every nullable column needed
// on the column path.
func TestEntity_EmptyStringIsAnAbsentAttribute(t *testing.T) {
	db, s, r := freshEntity(t)
	putItem(t, s, "a", map[string]string{"count": "7"})

	got, err := r.ScanAll(Backend{DB: db, Obj: s})
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if note := got.([]EntityRow)[0].Note; note != "" {
		t.Errorf("absent note read back as %q, want the empty string", note)
	}

	be := Backend{DB: db, Obj: s}
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "a", Note: "", Count: 7}},
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	items, err := s.Query(t.Context(), objstore.Query{Entity: "entityrows", Partition: entitySubsystem})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, present := items[0].Attrs["note"]; present {
		t.Errorf("the empty note was written as %q; an empty string must leave the "+
			"attribute OFF the item, or a tombstone test still finds it", items[0].Attrs["note"])
	}
}

// A number is stored as text and read back as a number. A value that is not a
// number is an ERROR rather than a zero: reading it as 0 would put a wrong
// max_uses into an export the operator then applies.
func TestEntity_IntAttributeRoundTripsAndRefusesNonsense(t *testing.T) {
	db, s, r := freshEntity(t)
	putItem(t, s, "a", map[string]string{"count": "42"})
	got, err := r.ScanAll(Backend{DB: db, Obj: s})
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if n := got.([]EntityRow)[0].Count; n != 42 {
		t.Errorf("Count = %d, want 42", n)
	}

	putItem(t, s, "b", map[string]string{"count": "many"})
	if _, err := r.ScanAll(Backend{DB: db, Obj: s}); err == nil {
		t.Fatal("ScanAll accepted count=\"many\"; a non-numeric attribute must be loud")
	} else if !strings.Contains(err.Error(), "not a number") {
		t.Errorf("error = %v, want it to name the value that is not a number", err)
	}
}

func TestEntity_ApplyReplacesTheProjection(t *testing.T) {
	db, s, r := freshEntity(t)
	putItem(t, s, "gone", map[string]string{"count": "1"})
	be := Backend{DB: db, Obj: s}

	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	next, err := Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "kept", Note: "n", Count: 3}},
	}, &ApplyOpts{Actor: "test", ManifestDigest: "d"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	want := []EntityRow{{Key: "kept", Note: "n", Count: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v (the wipe removed `gone`)", got, want)
	}
	again, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if again != next {
		t.Errorf("Apply returned %s and the projection hashes to %s", next, again)
	}

	// The apply's single audit row, recorded as the last fenced write of the
	// replace rather than inside a transaction there is none of.
	rows, err := s.Query(t.Context(), objstore.Query{Entity: "audit_log", Partition: "audit"})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(rows) != 1 || rows[0].Attrs["action"] != "config.apply" {
		t.Errorf("audit rows = %d %v, want exactly one config.apply", len(rows), rows)
	}
}

func TestEntity_ApplyRefusesAStaleChecksumAndForceOverrides(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}
	putItem(t, s, "a", map[string]string{"count": "1"})

	_, err := Apply(t.Context(), be, entitySubsystem, "sha256:stale", false, map[string]any{
		"entityrows": []EntityRow{},
	}, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Apply err = %v, want ErrChecksumMismatch", err)
	}
	// The body refused BEFORE writing, so the claim was released and the next
	// replace runs rather than answering ErrScopeHeld.
	if _, err := Apply(t.Context(), be, entitySubsystem, "sha256:stale", true, map[string]any{
		"entityrows": []EntityRow{},
	}, nil); err != nil {
		t.Fatalf("Apply --force after a refused one: %v", err)
	}
}

// The claim IS the lock. A scope somebody else holds refuses the apply and names
// them, where the column path would have blocked on a write lock.
func TestEntity_ApplyIsFencedByItsMarker(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := objstore.Replace(t.Context(), s, store.OwnerScope(entitySubsystem), "",
			func(ctx context.Context, rp *objstore.Replacer) error {
				close(held)
				<-release
				return nil
			})
		done <- err
	}()
	<-held
	_, err = Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "a", Count: 1}},
	}, nil)
	close(release)
	if rerr := <-done; rerr != nil {
		t.Fatalf("the holding replace: %v", rerr)
	}
	if !errors.Is(err, objstore.ErrScopeHeld) {
		t.Fatalf("Apply err = %v, want ErrScopeHeld: an apply that walked past a held "+
			"scope would write into a projection another one is part-way through replacing", err)
	}
	// And it wrote nothing.
	items, err := s.Query(t.Context(), objstore.Query{Entity: "entityrows", Partition: entitySubsystem})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("the fenced apply left %d items", len(items))
	}
}

func TestEntity_ExportSnapshotMatchesChecksum(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}
	putItem(t, s, "a", map[string]string{"note": "x", "count": "1"})

	manifest, sum, at, err := ExportSnapshot(t.Context(), be, entitySubsystem)
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if manifest == nil || at == "" {
		t.Fatalf("manifest=%v snapshotAt=%q", manifest, at)
	}
	want, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if sum != want {
		t.Errorf("checksum = %s, want %s: the consistent multi-read and the plain read "+
			"must hash the same document", sum, want)
	}
}

// A consistent read cannot be taken across a scope a replace is part-way
// through, and it says so rather than reporting a state that never existed.
func TestEntity_ExportSnapshotRefusesAHeldScope(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := objstore.Replace(t.Context(), s, store.OwnerScope(entitySubsystem), "",
			func(ctx context.Context, rp *objstore.Replacer) error {
				close(held)
				<-release
				return nil
			})
		done <- err
	}()
	<-held
	_, _, _, err := ExportSnapshot(t.Context(), be, entitySubsystem)
	close(release)
	if rerr := <-done; rerr != nil {
		t.Fatalf("the holding replace: %v", rerr)
	}
	if !errors.Is(err, objstore.ErrScopeHeld) {
		t.Errorf("ExportSnapshot err = %v, want ErrScopeHeld", err)
	}
}

// Two rows at one coordinate would silently become one item, so the whole set is
// checked before any batch boundary decides which two collide.
func TestEntity_ApplyRefusesTwoRowsAtOneCoordinate(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	_, err = Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "a", Count: 1}, {Key: "a", Count: 2}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "two rows carry") {
		t.Fatalf("Apply err = %v, want a refusal naming the duplicate coordinate", err)
	}
}

// A row whose sort-key field is empty has no coordinate, and every such row
// would address one item.
func TestEntity_ApplyRefusesARowWithNoCoordinate(t *testing.T) {
	db, s, _ := freshEntity(t)
	be := Backend{DB: db, Obj: s}
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	_, err = Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "", Count: 1}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "sort key") {
		t.Fatalf("Apply err = %v, want a refusal naming the missing sort key", err)
	}
}

// Apply is atomic per SUBSYSTEM, so the two shapes may not share one.
func TestEntity_MixedSubsystemIsRefused(t *testing.T) {
	db, s, _ := freshEntity(t)
	Register(Resource{
		Name:     "columnrows",
		Table:    "columnrows",
		DB:       entitySubsystem,
		RowType:  reflect.TypeFor[TestRow](),
		PKFields: []string{"Kind", "Name"},
	})
	be := Backend{DB: db, Obj: s}
	_, err := Apply(t.Context(), be, entitySubsystem, "", true, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "mixes shapes") {
		t.Fatalf("Apply err = %v, want the mixed-subsystem refusal", err)
	}
	if _, _, _, err := ExportSnapshot(t.Context(), be, entitySubsystem); err == nil ||
		!strings.Contains(err.Error(), "mixes shapes") {
		t.Errorf("ExportSnapshot err = %v, want the mixed-subsystem refusal", err)
	}
}

// Every refusal below is a caller fault and stops the process at Register, so a
// misdeclared resource never serves a row it would read wrong.
func TestEntity_RegisterRefusesAMisdeclaredShape(t *testing.T) {
	base := func() Resource {
		return Resource{
			Name: "bad", Table: "bad", DB: entitySubsystem,
			RowType: reflect.TypeFor[EntityRow](), PKFields: []string{"Key"},
			EntitySortKey: []string{"Key"},
		}
	}
	type badRow struct {
		Key  string   `db:"key"`
		List []string `db:"list"`
	}
	cases := []struct {
		name string
		want string
		of   func(Resource) Resource
	}{
		{"no subsystem", "names no owner subsystem",
			func(r Resource) Resource { r.DB = ""; return r }},
		{"sort key is not a field", "is not a field",
			func(r Resource) Resource { r.EntitySortKey = []string{"Nope"}; return r }},
		{"sort key is not text", "coordinate is text",
			func(r Resource) Resource { r.EntitySortKey = []string{"Count"}; return r }},
		{"row filter", "conjunction with no OR",
			func(r Resource) Resource { r.RowFilter = "count > 0"; return r }},
		{"scope", "unbounded read-then-delete",
			func(r Resource) Resource { r.Scope = ScopeSpec{Field: "Note"}; return r }},
		{"stamped fields", "only null an item has",
			func(r Resource) Resource { r.StampedFields = []string{"Note"}; return r }},
		{"hooks", "declares Hooks", func(r Resource) Resource {
			r.Hooks = Hooks{ColumnOverride: map[string]ColumnHook{
				"Note": {Read: "COALESCE(note,'')"}}}
			return r
		}},
		{"unrenderable attribute", "renders string and int only", func(r Resource) Resource {
			r.RowType = reflect.TypeFor[badRow]()
			r.PKFields = []string{"Key"}
			return r
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("Register accepted %s", tc.name)
				}
				if msg, _ := got.(string); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %v, want it to contain %q", got, tc.want)
				}
			}()
			Register(tc.of(base()))
		})
	}
}

// EnsureSubsystem is a NO-OP for a subsystem still on the column path, so an
// opener calls it unconditionally and the entity set follows the registrations
// rather than a second list beside them.
func TestEntity_EnsureSubsystemIsQuietForColumnShapes(t *testing.T) {
	db, _ := freshEngine(t)
	s := sqlstore.New(db)
	if err := EnsureSubsystem(t.Context(), s, s, testSubsystem); err != nil {
		t.Fatalf("EnsureSubsystem on a column subsystem: %v", err)
	}
	var n int
	if err := db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?",
		store.ScopeEntity).Scan(&n); err != nil {
		t.Skipf("shape probe is SQLite's: %v", err)
	}
	if n != 0 {
		t.Errorf("%s was created for a subsystem with no entity-backed resource", store.ScopeEntity)
	}
}

// The four column writes take a *sql.Tx, and an entity-backed subsystem's apply
// takes a claim rather than a transaction — so reaching one means a caller went
// round the dispatch, and the answer names the shape rather than a column the
// engine cannot find.
func TestEntity_ColumnWritesRefuseAnEntityResource(t *testing.T) {
	db, _, r := freshEntity(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // nothing here commits
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"Insert", func() error { return r.Insert(t.Context(), tx, EntityRow{Key: "a"}) }},
		{"InsertAll", func() error { return r.InsertAll(t.Context(), tx, []EntityRow{{Key: "a"}}) }},
		{"DeleteScope", func() error { return r.DeleteScope(t.Context(), tx, "a") }},
		{"DeleteAll", func() error { return r.DeleteAll(t.Context(), tx) }},
	} {
		err := tc.run()
		if err == nil {
			t.Errorf("%s wrote columns to an entity-backed resource", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "is entity-backed") {
			t.Errorf("%s err = %v, want it to name the shape", tc.name, err)
		}
	}
}

// --- the composite coordinate ------------------------------------------
//
// network_rules is keyed (folder, target) and installed_packages (folder,
// name), so a coordinate is a LIST of fields joined by a declared separator
// (spec 6/40 §1). The bodies below pin the three things that makes true: the
// tuple survives the round trip through the key, an instance-wide row whose
// first field is empty still has a coordinate, and a value carrying the
// separator is REFUSED rather than joined.

// CompositeRow is a synthetic (folder, target) resource: two coordinate fields
// and one ordinary attribute.
type CompositeRow struct {
	Folder string `db:"folder" yaml:"folder"`
	Target string `db:"target" yaml:"target"`
	By     string `db:"by"     yaml:"by,omitempty"`
}

// compositeSep is a byte no folder and no hostname carries. It is NOT "/",
// because a folder path is made of those — which is the whole reason the
// separator is declared per resource rather than fixed by the engine.
const compositeSep = "\x1f"

func freshComposite(t *testing.T) (*sql.DB, objstore.Store, *Resource) {
	t.Helper()
	reset()
	db := dbtest.Open(t)
	s := sqlstore.New(db)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "composites",
		Table:         "composites",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[CompositeRow](),
		PKFields:      []string{"Folder", "Target"},
		EntitySortKey: []string{"Folder", "Target"},
		EntityKeySep:  compositeSep,
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return db, s, r
}

// The pair is the coordinate, so two rows sharing ONE of the two are two items.
// This is what `target` alone could not give: krons carries 15 network_rules
// rows over 10 targets, so a key on target would have collided five of them.
func TestEntity_CompositeCoordinateKeepsRowsApart(t *testing.T) {
	db, s, r := freshComposite(t)
	be := Backend{DB: db, Obj: s}
	rows := []CompositeRow{
		{Folder: "corp/eng", Target: "api.example.com", By: "sre"},
		{Folder: "corp/sre", Target: "api.example.com", By: "sre"},
		// The instance-wide rule: folder "" is the sentinel network_rules and
		// installed_packages both use, and it still has a coordinate.
		{Folder: "", Target: "api.example.com", By: "migration"},
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"composites": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	back := got.([]CompositeRow)
	if len(back) != 3 {
		t.Fatalf("read %d rows, want 3 — one item per (folder, target): %v", len(back), back)
	}
	want := map[string]string{"corp/eng": "sre", "corp/sre": "sre", "": "migration"}
	for _, row := range back {
		if row.Target != "api.example.com" {
			t.Errorf("target = %q, want it back out of the coordinate", row.Target)
		}
		if by, ok := want[row.Folder]; !ok || by != row.By {
			t.Errorf("row %+v: folder did not come back out of the coordinate", row)
		}
		delete(want, row.Folder)
	}
	if len(want) > 0 {
		t.Errorf("rows never came back: %v", want)
	}
}

// A value carrying the separator is REFUSED at write time.
//
// There are two guards, one at each end, and this is the one that keeps a
// corrupt coordinate OUT OF THE STORE: the read below refuses a key that does
// not split into as many parts as the coordinate has, so without this refusal
// the row is written and then unreadable — an export that fails over a row an
// apply put there. Two rows joining to ONE key is the other half, and that one
// the read cannot see at all, because the key it produced splits cleanly.
func TestEntity_CompositeRefusesASeparatorInAValue(t *testing.T) {
	db, s, r := freshComposite(t)
	be := Backend{DB: db, Obj: s}

	// One row, nothing to collide with: the damage is entirely in the write.
	lone := []CompositeRow{{Folder: "corp/eng", Target: "a" + compositeSep + "b", By: "sre"}}
	_, err := r.entityPuts(lone)
	if err == nil {
		t.Fatal("entityPuts joined a value that carries the separator, so the key it " +
			"wrote does not split back into the row it came from")
	}
	for _, want := range []string{"separates", "one item", "target"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
	// The refusal reaches an operator through Apply, not only the renderer, and
	// it lands BEFORE anything is written: the projection is still empty.
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"composites": lone}, nil); err == nil {
		t.Fatal("Apply wrote a row whose coordinate carries the separator")
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll after the refused apply: %v", err)
	}
	if n := len(got.([]CompositeRow)); n != 0 {
		t.Errorf("%d rows are stored after a refused apply, want 0", n)
	}

	// Two rows joining to one key: the read cannot tell, because "a<sep>b<sep>c"
	// is a key of the right shape for a three-field coordinate and this one has
	// two. The write is the only place to catch it.
	collide := []CompositeRow{
		{Folder: "a" + compositeSep + "b", Target: "c", By: "first"},
		{Folder: "a", Target: "b" + compositeSep + "c", By: "second"},
	}
	if _, err := r.entityPuts(collide); err == nil {
		t.Fatal("entityPuts joined two rows onto one coordinate")
	}
}

// A stored key that does not split into as many parts as the coordinate has is
// a LOUD read fault. Half a coordinate is a row nobody wrote, and answering it
// as one would put it in an export the operator then applies.
func TestEntity_CompositeRefusesAKeyThatDoesNotSplit(t *testing.T) {
	db, s, r := freshComposite(t)
	committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: "composites",
		Key:   objstore.Key{Partition: entitySubsystem, Sort: "no-separator-here"},
		Attrs: map[string]string{"by": "somebody else"},
	}})
	if err != nil || !committed {
		t.Fatalf("put: committed=%v err=%v", committed, err)
	}
	if _, err := r.ScanAll(Backend{DB: db, Obj: s}); err == nil ||
		!strings.Contains(err.Error(), "splits on") {
		t.Errorf("ScanAll err = %v, want it to name the key that does not split", err)
	}
}

// The declaration's own refusals: a composite key with nothing to join it, a
// separator beside a single field, and a field named twice.
func TestEntity_RegisterRefusesAMisdeclaredCoordinate(t *testing.T) {
	base := func() Resource {
		return Resource{
			Name: "bad", Table: "bad", DB: entitySubsystem,
			RowType: reflect.TypeFor[CompositeRow](), PKFields: []string{"Folder"},
			EntitySortKey: []string{"Folder", "Target"}, EntityKeySep: compositeSep,
		}
	}
	cases := []struct {
		name string
		want string
		of   func(Resource) Resource
	}{
		{"composite with no separator", "declares no EntityKeySep",
			func(r Resource) Resource { r.EntityKeySep = ""; return r }},
		{"separator with one field", "there is nothing to join",
			func(r Resource) Resource { r.EntitySortKey = []string{"Folder"}; return r }},
		{"separator with no key", "a separator with nothing to join",
			func(r Resource) Resource { r.EntitySortKey = nil; return r }},
		{"one field twice", "cannot be two parts of a coordinate",
			func(r Resource) Resource { r.EntitySortKey = []string{"Folder", "Folder"}; return r }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("Register accepted %s", tc.name)
				}
				if msg, _ := got.(string); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %v, want it to contain %q", got, tc.want)
				}
			}()
			Register(tc.of(base()))
		})
	}
}

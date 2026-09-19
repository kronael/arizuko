package resreg

// What the engine does with a row whose storage is an ITEM, in isolation — a
// synthetic entity-backed resource, no arizuko-specific declaration. The
// coordinate, the codec, the stamped field, the declared default and the scoped
// delete inside the claim are here; the manifest lifecycle around them is
// engine_test.go's.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
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
func freshEntity(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
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
	return s, r
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
	s, r := freshEntity(t)
	putItem(t, s, "b", map[string]string{"note": "second", "count": "2"})
	putItem(t, s, "a", map[string]string{"note": "first", "count": "1"})

	got, err := r.ScanAll(On(s))
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
	s, r := freshEntity(t)
	putItem(t, s, "real", map[string]string{"key": "impostor", "count": "0"})

	got, err := r.ScanAll(On(s))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]EntityRow)
	if rows[0].Key != "real" {
		t.Errorf("Key = %q, want %q", rows[0].Key, "real")
	}
}

// An absent attribute IS the empty string, in both directions. That is why a
// nullable value needs no COALESCE on read and no nil on write.
func TestEntity_EmptyStringIsAnAbsentAttribute(t *testing.T) {
	s, r := freshEntity(t)
	putItem(t, s, "a", map[string]string{"count": "7"})

	got, err := r.ScanAll(On(s))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if note := got.([]EntityRow)[0].Note; note != "" {
		t.Errorf("absent note read back as %q, want the empty string", note)
	}

	be := On(s)
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
	s, r := freshEntity(t)
	putItem(t, s, "a", map[string]string{"count": "42"})
	got, err := r.ScanAll(On(s))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if n := got.([]EntityRow)[0].Count; n != 42 {
		t.Errorf("Count = %d, want 42", n)
	}

	putItem(t, s, "b", map[string]string{"count": "many"})
	if _, err := r.ScanAll(On(s)); err == nil {
		t.Fatal("ScanAll accepted count=\"many\"; a non-numeric attribute must be loud")
	} else if !strings.Contains(err.Error(), "not a number") {
		t.Errorf("error = %v, want it to name the value that is not a number", err)
	}
}

func TestEntity_ApplyReplacesTheProjection(t *testing.T) {
	s, r := freshEntity(t)
	putItem(t, s, "gone", map[string]string{"count": "1"})
	be := On(s)

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
	s, _ := freshEntity(t)
	be := On(s)
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
// them rather than blocking on it.
func TestEntity_ApplyIsFencedByItsMarker(t *testing.T) {
	s, _ := freshEntity(t)
	be := On(s)
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := objstore.Replace(t.Context(), s, store.OwnerScope(entitySubsystem), "",
			objstore.Claim{Intent: "not an apply"},
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
	s, _ := freshEntity(t)
	be := On(s)
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
	s, _ := freshEntity(t)
	be := On(s)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := objstore.Replace(t.Context(), s, store.OwnerScope(entitySubsystem), "",
			objstore.Claim{Intent: "not an apply"},
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
	s, _ := freshEntity(t)
	be := On(s)
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
	s, _ := freshEntity(t)
	be := On(s)
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

// Apply and ExportSnapshot run over ITEMS, so a subsystem holding a resource
// that declares no entity shape is refused before either of them writes
// anything. resreg/resources/shape_test.go holds the REGISTERED set to the same
// rule; this is the gate in front of the two verbs.
func TestEntity_AColumnShapedResourceRefusesTheSubsystem(t *testing.T) {
	s, _ := freshEntity(t)
	Register(Resource{
		Name:     "columnrows",
		Table:    "columnrows",
		DB:       entitySubsystem,
		RowType:  reflect.TypeFor[TestRow](),
		PKFields: []string{"Kind", "Name"},
	})
	be := On(s)
	_, err := Apply(t.Context(), be, entitySubsystem, "", true, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "columnrows") ||
		!strings.Contains(err.Error(), "declare no EntitySortKey") {
		t.Fatalf("Apply err = %v, want a refusal naming columnrows and what it failed to "+
			"declare", err)
	}
	if _, _, _, err := ExportSnapshot(t.Context(), be, entitySubsystem); err == nil ||
		!strings.Contains(err.Error(), "columnrows") {
		t.Errorf("ExportSnapshot err = %v, want the same refusal", err)
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
		{"a filter on the coordinate", "part of its coordinate",
			func(r Resource) Resource {
				r.RowFilter = []objstore.Cmp{{Attr: "key", Op: objstore.Eq, Value: "a"}}
				return r
			}},
		{"a scope column of its own", "named by its db: tag and nothing else",
			func(r Resource) Resource {
				r.Scope = ScopeSpec{Field: "Note", Column: "other"}
				return r
			}},
		{"a stamped coordinate", "invents addresses a new item",
			func(r Resource) Resource { r.StampedFields = []string{"Key"}; return r }},
		{"a column override", "declares AfterScan or ColumnOverride",
			func(r Resource) Resource {
				r.Hooks = Hooks{ColumnOverride: map[string]ColumnHook{
					"Note": {Read: "COALESCE(note,'')"}}}
				return r
			}},
		{"an after-scan hook", "declares AfterScan or ColumnOverride",
			func(r Resource) Resource {
				r.Hooks = Hooks{AfterScan: func(any) error { return nil }}
				return r
			}},
		{"unrenderable attribute", "declares its encoding in the db: tag", func(r Resource) Resource {
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

// EnsureSubsystem creates NOTHING for a subsystem no entity-backed resource
// names, so an opener calls it unconditionally and the entity set follows the
// registrations rather than a second list beside them.
func TestEntity_EnsureSubsystemIsQuietWithNoEntityBackedResource(t *testing.T) {
	reset()
	s := dyntest.Store(t)
	Register(Resource{
		Name:     "columnrows",
		Table:    "columnrows",
		DB:       entitySubsystem,
		RowType:  reflect.TypeFor[TestRow](),
		PKFields: []string{"Kind", "Name"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	entities, err := s.Entities(t.Context())
	if err != nil {
		t.Fatalf("look for %s: %v", store.ScopeEntity, err)
	}
	if slices.Contains(entities, store.ScopeEntity) {
		t.Errorf("%s was created for a subsystem with no entity-backed resource", store.ScopeEntity)
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

func freshComposite(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
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
	return s, r
}

// The pair is the coordinate, so two rows sharing ONE of the two are two items.
// This is what `target` alone could not give: krons carries 15 network_rules
// rows over 10 targets, so a key on target would have collided five of them.
func TestEntity_CompositeCoordinateKeepsRowsApart(t *testing.T) {
	s, r := freshComposite(t)
	be := On(s)
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
	s, r := freshComposite(t)
	be := On(s)

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
	s, r := freshComposite(t)
	committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: "composites",
		Key:   objstore.Key{Partition: entitySubsystem, Sort: "no-separator-here"},
		Attrs: map[string]string{"by": "somebody else"},
	}})
	if err != nil || !committed {
		t.Fatalf("put: committed=%v err=%v", committed, err)
	}
	if _, err := r.ScanAll(On(s)); err == nil ||
		!strings.Contains(err.Error(), "splits on") {
		t.Errorf("ScanAll err = %v, want it to name the key that does not split", err)
	}
}

// The declaration's own refusals: a composite key with nothing to join it, a
// separator beside a single field, a field named twice, and a PK that does not
// cover the coordinate.
func TestEntity_RegisterRefusesAMisdeclaredCoordinate(t *testing.T) {
	base := func() Resource {
		return Resource{
			Name: "bad", Table: "bad", DB: entitySubsystem,
			RowType: reflect.TypeFor[CompositeRow](), PKFields: []string{"Folder", "Target"},
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
		// A PK narrower than the coordinate is the one misdeclaration that reads
		// as healthy: every verb works and the rows are all there, but Diff files
		// two items under one identity and reports one of them (BUGS L260).
		{"pk narrower than the coordinate", `omits "Target"`,
			func(r Resource) Resource { r.PKFields = []string{"Folder"}; return r }},
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

// --- the padded coordinate ----------------------------------------------
//
// routes is keyed (seq, target, match) and `seq` is an integer. A key is
// compared as TEXT, so the rendering has to make byte order and numeric order
// the same thing — the rule objstore.OrderedSort enforces for an instant
// (BUGS L97), applied to a number (spec 6/40 §"The four coordinates").

// PaddedRow is that coordinate in miniature: a number first, a string second.
type PaddedRow struct {
	Seq    int    `db:"seq,pad" yaml:"seq"`
	Target string `db:"target"  yaml:"target"`
	By     string `db:"by"      yaml:"by,omitempty"`
}

func freshPadded(t *testing.T) (Backend, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "paddedrows",
		Table:         "paddedrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[PaddedRow](),
		PKFields:      []string{"Seq", "Target"},
		EntitySortKey: []string{"Seq", "Target"},
		EntityKeySep:  compositeSep,
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), r
}

// A driver answers in SORT-KEY order, so the read comes back in numeric order
// or it does not. 2 before 10 is the whole claim: strconv's rendering puts "10"
// first, and the route table's meaning is "lower seq wins, first match".
func TestEntity_PaddedCoordinateSortsNumerically(t *testing.T) {
	be, r := freshPadded(t)
	rows := []PaddedRow{
		{Seq: 100, Target: "z"},
		{Seq: 2, Target: "b"},
		{Seq: 10, Target: "c"},
		{Seq: 0, Target: "a"},
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"paddedrows": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	var seqs []int
	for _, row := range got.([]PaddedRow) {
		seqs = append(seqs, row.Seq)
	}
	want := []int{0, 2, 10, 100}
	if !slices.Equal(seqs, want) {
		t.Errorf("seq order = %v, want %v — the key is compared as text, so the number "+
			"has to be rendered at a fixed width", seqs, want)
	}
}

// A value the padding cannot order is REFUSED rather than written, at the same
// end the separator is: the write is the only place that can see it.
func TestEntity_PaddedCoordinateRefusesWhatItCannotOrder(t *testing.T) {
	be, r := freshPadded(t)
	for _, tc := range []struct {
		name string
		row  PaddedRow
		want string
	}{
		{"negative", PaddedRow{Seq: -1, Target: "a"}, "is negative"},
		{"wider than the padding", PaddedRow{Seq: 12345678901, Target: "a"}, "wider than the 10 digits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.entityPuts([]PaddedRow{tc.row})
			if err == nil {
				t.Fatal("entityPuts rendered a value that does not sort in numeric order")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
			if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
				map[string]any{"paddedrows": []PaddedRow{tc.row}}, nil); err == nil {
				t.Error("Apply wrote it anyway")
			}
		})
	}
}

// The padding belongs to a COORDINATE. Declaring it on an attribute, on a
// column-shaped resource, or on a field that is not a number is refused at
// Register — each of the three is a second rendering of a value that already
// has one, which is the drift codec.go exists to prevent.
func TestEntity_RegisterRefusesAMisplacedPadding(t *testing.T) {
	type attrRow struct {
		Key   string `db:"key"`
		Count int    `db:"count,pad"`
	}
	type textRow struct {
		Key  string `db:"key"`
		Note string `db:"note,pad"`
	}
	cases := []struct {
		name string
		want string
		of   func() Resource
	}{
		{"on an attribute", "not part of the coordinate", func() Resource {
			return Resource{Name: "bad", Table: "bad", DB: entitySubsystem,
				RowType: reflect.TypeFor[attrRow](), PKFields: []string{"Key"},
				EntitySortKey: []string{"Key"}}
		}},
		{"on a column-backed resource", "column-backed", func() Resource {
			return Resource{Name: "bad", Table: "bad", DB: entitySubsystem,
				RowType: reflect.TypeFor[attrRow](), PKFields: []string{"Key"}}
		}},
		{"on a field that is not a number", "which pads a number", func() Resource {
			return Resource{Name: "bad", Table: "bad", DB: entitySubsystem,
				RowType: reflect.TypeFor[textRow](), PKFields: []string{"Key"},
				EntitySortKey: []string{"Key"}}
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
			Register(tc.of())
		})
	}
}

// --- the stamped field --------------------------------------------------
//
// Six routd resources declare StampedFields so the engine fills created_at on
// insert (BUGS F49). An entity has no engine between the caller and the item,
// so the storage layer stamps it — and the declaration stays, because it is
// what tells an operator's manifest the field is not theirs to set (spec 6/40
// §"What a registered resource still needs", answer 2).

// StampedRow is a synthetic resource with one server-owned instant.
type StampedRow struct {
	Key     string `db:"key"     yaml:"key"`
	Note    string `db:"note"    yaml:"note,omitempty"`
	Created string `db:"created" yaml:"created,omitempty"`
}

func freshStamped(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "stampedrows",
		Table:         "stampedrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[StampedRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
		StampedFields: []string{"Created"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return s, r
}

// A row that leaves the stamped field empty gets one, and a row that carries a
// value keeps it.
func TestEntity_StampedFieldIsFilledByTheWriter(t *testing.T) {
	s, r := freshStamped(t)
	be := On(s)
	rows := []StampedRow{
		{Key: "fresh", Note: "no instant of its own"},
		{Key: "kept", Created: "2020-01-01T00:00:00.000000000Z"},
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"stampedrows": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	by := map[string]StampedRow{}
	for _, row := range got.([]StampedRow) {
		by[row.Key] = row
	}
	if by["fresh"].Created == "" {
		t.Error("the writer left the stamped field empty; nothing else fills it on this path")
	}
	if got, want := len(by["fresh"].Created), len(core.NowTS()); got != want {
		t.Errorf("the stamp is %d bytes and core.TS is %d; a stored instant is compared "+
			"as TEXT, so a short one sorts into the wrong place (BUGS L97)", got, want)
	}
	if _, err := time.Parse(core.TSLayout, by["fresh"].Created); err != nil {
		t.Errorf("the stamp %q is not core.TS: %v", by["fresh"].Created, err)
	}
	if by["kept"].Created != "2020-01-01T00:00:00.000000000Z" {
		t.Errorf("Created = %q, want the value the row carried — the writer fills an "+
			"EMPTY field, it does not overwrite one", by["kept"].Created)
	}
}

// The declaration stays for the reason it exists: Diff ignores a stamped field,
// so a hand-written manifest that omits `created` reads as unchanged against a
// live stamped item instead of phantom-updating on every plan (BUGS F49).
func TestEntity_StampedFieldIsIgnoredByDiff(t *testing.T) {
	s, r := freshStamped(t)
	be := On(s)
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"stampedrows": []StampedRow{{Key: "a", Note: "n"}}}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	d, err := r.Diff(be, []StampedRow{{Key: "a", Note: "n"}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Changed() {
		t.Errorf("a manifest that omits the stamped field reads as changed: %+v", d)
	}
}

// --- the minted number --------------------------------------------------

// NumberedRow is routes' id in miniature: a coordinate, and a server-owned
// number the manifest may omit.
type NumberedRow struct {
	Key string `db:"key" yaml:"key"`
	ID  int    `db:"id"  yaml:"id,omitempty"`
}

func freshNumbered(t *testing.T) (Backend, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "numberedrows",
		Table:         "numberedrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[NumberedRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
		StampedFields: []string{"ID"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), r
}

// A row that states a number keeps it, and a row that omits one gets a number
// nothing else in the set holds — which is what AUTOINCREMENT gave the column
// path and what `delete_route(id)` addresses.
func TestEntity_StampedNumberIsMintedByTheWriter(t *testing.T) {
	be, r := freshNumbered(t)
	rows := []NumberedRow{{Key: "stated", ID: 7}, {Key: "omitted"}, {Key: "also-omitted"}}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"numberedrows": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	by := map[string]int{}
	for _, row := range got.([]NumberedRow) {
		by[row.Key] = row.ID
	}
	if by["stated"] != 7 {
		t.Errorf("stated id = %d, want the value the row carried", by["stated"])
	}
	seen := map[int]string{7: "stated"}
	for _, key := range []string{"omitted", "also-omitted"} {
		id := by[key]
		if id == 0 {
			t.Errorf("%s kept id 0; an entity has no sequence, so the writer mints one", key)
		}
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s both carry id %d, so delete_route(%d) names two rows",
				key, other, id, id)
		}
		seen[id] = key
	}
	if rows[1].ID != 0 {
		t.Errorf("the manifest row now carries id %d; the mint was written into the "+
			"caller's own slice", rows[1].ID)
	}
}

// A resource that mints a number may not scope its apply: a scoped apply wipes
// only the scopes its manifest names, so the largest number the manifest states
// is not the largest one the resource holds.
func TestEntity_RegisterRefusesAMintedNumberOnAScopedResource(t *testing.T) {
	type scopedNumberRow struct {
		Key    string `db:"key"`
		Folder string `db:"folder"`
		ID     int    `db:"id"`
	}
	reset()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("Register accepted a minted number beside a scope")
		}
		if msg, _ := got.(string); !strings.Contains(msg, "would collide with a row it never touched") {
			t.Errorf("panic = %v, want it to name the collision", got)
		}
	}()
	Register(Resource{
		Name: "bad", Table: "bad", DB: entitySubsystem,
		RowType: reflect.TypeFor[scopedNumberRow](), PKFields: []string{"Key"},
		EntitySortKey: []string{"Key"},
		Scope:         ScopeSpec{Field: "Folder"},
		StampedFields: []string{"ID"},
	})
}

// --- the declared row filter --------------------------------------------
//
// A filter narrows the manifest-visible projection, and the SAME declaration has
// to narrow the read and both deletes: a filter the read honours but a delete
// does not widens that delete past the projection it was computed from. On this
// path a delete set is a READ, so all three are one Where (spec 6/40 answer 6).

// FilteredRow carries a scope and the attribute its own filter compares.
type FilteredRow struct {
	Key    string `db:"key"    yaml:"key"`
	Folder string `db:"folder" yaml:"folder,omitempty"`
	Kind   string `db:"kind"   yaml:"kind,omitempty"`
}

func freshFiltered(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "filteredrows",
		Table:         "filteredrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[FilteredRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
		Scope:         ScopeSpec{Field: "Folder"},
		RowFilter:     []objstore.Cmp{{Attr: "kind", Op: objstore.Ne, Value: "excluded"}},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return s, r
}

func putFiltered(t *testing.T, s objstore.Store, sort string, attrs map[string]string) {
	t.Helper()
	committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: "filteredrows",
		Key:   objstore.Key{Partition: entitySubsystem, Sort: sort},
		Attrs: attrs,
	}})
	if err != nil || !committed {
		t.Fatalf("put %s: committed=%v err=%v", sort, committed, err)
	}
}

func TestEntity_RowFilterNarrowsTheRead(t *testing.T) {
	s, r := freshFiltered(t)
	putFiltered(t, s, "kept", map[string]string{"folder": "f1", "kind": "config"})
	putFiltered(t, s, "hidden", map[string]string{"folder": "f1", "kind": "excluded"})

	got, err := r.ScanAll(On(s))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]FilteredRow)
	if len(rows) != 1 || rows[0].Key != "kept" {
		t.Fatalf("ScanAll = %+v, want only the unfiltered item", rows)
	}
}

// The wipe is the delete half, and it is built from a read — so the filter that
// narrows the read narrows it, and an excluded item survives an apply that
// replaces everything else.
func TestEntity_RowFilterNarrowsTheWipeAndTheScopedDelete(t *testing.T) {
	s, r := freshFiltered(t)
	be := On(s)
	putFiltered(t, s, "hidden", map[string]string{"folder": "f1", "kind": "excluded"})
	putFiltered(t, s, "stale", map[string]string{"folder": "f1", "kind": "config"})
	putFiltered(t, s, "other", map[string]string{"folder": "f2", "kind": "config"})

	// A manifest naming f1 only: the scoped delete takes f1's unfiltered rows.
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"filteredrows": []FilteredRow{{Key: "fresh", Folder: "f1", Kind: "config"}}},
		nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	live, err := s.Query(t.Context(), objstore.Query{
		Entity: "filteredrows", Partition: entitySubsystem})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	keys := map[string]bool{}
	for _, it := range live {
		keys[it.Key.Sort] = true
	}
	if !keys["hidden"] {
		t.Error("the apply deleted an excluded item; a filter the read honours and the " +
			"delete does not takes rows the manifest never saw")
	}
	if keys["stale"] {
		t.Error("the apply left f1's replaced row behind")
	}
	if !keys["other"] {
		t.Error("the apply reached a scope the manifest never mentioned")
	}
	if !keys["fresh"] {
		t.Error("the apply did not write the manifest's row")
	}

	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if len(got.([]FilteredRow)) != 2 {
		t.Errorf("projection = %+v, want the two unfiltered rows", got)
	}
}

// The wholesale wipe is the other delete, reached by an explicitly-empty row
// list — the operator declaring "no rows for this resource". It is built from a
// read too, so the same filter narrows it.
func TestEntity_RowFilterNarrowsTheWipe(t *testing.T) {
	s, r := freshFiltered(t)
	be := On(s)
	putFiltered(t, s, "hidden", map[string]string{"folder": "f1", "kind": "excluded"})
	putFiltered(t, s, "stale", map[string]string{"folder": "f1", "kind": "config"})

	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"filteredrows": []FilteredRow{}}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	live, err := s.Query(t.Context(), objstore.Query{
		Entity: "filteredrows", Partition: entitySubsystem})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(live) != 1 || live[0].Key.Sort != "hidden" {
		t.Fatalf("after the wipe the entity holds %+v, want only the excluded item; a "+
			"wipe that ignores the filter takes rows the manifest never saw", live)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if n := len(got.([]FilteredRow)); n != 0 {
		t.Errorf("projection holds %d rows after an empty manifest, want 0", n)
	}
}

// An absent attribute answers no comparison, so a row that omitted the one its
// own filter tests would write itself out of the projection: invisible to every
// export, and untouched by the apply that should have replaced it. The writer
// therefore stores it even when it is empty, which is the same guarantee routd
// migration 0047 gives the rows it touched.
func TestEntity_RowFilterAttributeIsWrittenEvenWhenEmpty(t *testing.T) {
	s, r := freshFiltered(t)
	be := On(s)
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"filteredrows": []FilteredRow{{Key: "bare", Folder: "f1"}}},
		nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	items, err := s.Query(t.Context(), objstore.Query{
		Entity: "filteredrows", Partition: entitySubsystem})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("stored %d items, want 1", len(items))
	}
	if _, ok := items[0].Attrs["kind"]; !ok {
		t.Error("the item carries no `kind` attribute; the filter compares it, and an " +
			"item without it is a row no export shows and no apply replaces")
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	if len(got.([]FilteredRow)) != 1 {
		t.Errorf("projection = %+v, want the row back", got)
	}
}

// --- the declared default -----------------------------------------------
//
// `acl`, `groups` and `scheduled_tasks` fill a value an operator's manifest may
// omit — effect='allow', product='assistant', status='active' — with a declared
// hook. An entity has no engine between the caller and the item, so the storage
// layer applies it, exactly as it applies a stamp (spec 6/40 §"What a registered
// resource still needs", answer 5).

// DefaultedRow is one field the writer fills when the row leaves it empty.
type DefaultedRow struct {
	Key    string `db:"key"    yaml:"key"`
	Effect string `db:"effect" yaml:"effect,omitempty"`
}

func freshDefaulted(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "defaultedrows",
		Table:         "defaultedrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[DefaultedRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
		Hooks: Hooks{
			BeforeInsert: func(row any) error {
				p := row.(*DefaultedRow)
				if p.Effect == "" {
					p.Effect = "allow"
				}
				return nil
			},
		},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return s, r
}

// A row that omits the field gets the default, and a row that states one keeps
// it.
func TestEntity_DeclaredDefaultIsFilledByTheWriter(t *testing.T) {
	s, r := freshDefaulted(t)
	be := On(s)
	rows := []DefaultedRow{{Key: "omitted"}, {Key: "stated", Effect: "deny"}}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"defaultedrows": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	by := map[string]string{}
	for _, row := range got.([]DefaultedRow) {
		by[row.Key] = row.Effect
	}
	if by["omitted"] != "allow" {
		t.Errorf("effect = %q, want the declared default; nothing else fills it on this "+
			"path, because an entity has no engine between the caller and the item", by["omitted"])
	}
	if by["stated"] != "deny" {
		t.Errorf("effect = %q, want the value the row carried — the writer fills an EMPTY "+
			"field, it does not overwrite one", by["stated"])
	}
}

// The hook mutates a row POINTER, and the rows it is handed are the manifest
// the apply is still reading — every later resource's checksum, its Diff and
// its audit counts read the same slice. So the row is copied first, and this is
// the check that it is.
func TestEntity_DeclaredDefaultDoesNotEditTheManifest(t *testing.T) {
	s, _ := freshDefaulted(t)
	be := On(s)
	rows := []DefaultedRow{{Key: "omitted"}}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"defaultedrows": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rows[0].Effect != "" {
		t.Errorf("the manifest row now carries effect=%q; the default was written into "+
			"the caller's own slice", rows[0].Effect)
	}
}

// --- the declared set check ---------------------------------------------

// freshValidated registers a resource whose declared set check refuses any row
// set carrying the key "bad".
func freshValidated(t *testing.T) (Backend, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name:          "validatedrows",
		Table:         "validatedrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[DefaultedRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
		Hooks: Hooks{
			ValidateRows: func(_ context.Context, rows any) error {
				for _, row := range rows.([]DefaultedRow) {
					if row.Key == "bad" {
						return errors.New("the set carries a row this resource refuses")
					}
				}
				return nil
			},
		},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), r
}

// A refused set leaves the resource EXACTLY as it was. That is the whole point
// of running the check before the wipe: this path has no pre-image and no
// rollback, so a check between the delete and the puts would hand an operator a
// resource with its rows gone and none written.
func TestEntity_ValidateRowsRefusesBeforeTheWipe(t *testing.T) {
	be, r := freshValidated(t)
	good := []DefaultedRow{{Key: "kept", Effect: "allow"}}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"validatedrows": good}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	_, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"validatedrows": []DefaultedRow{{Key: "bad"}}}, nil)
	if err == nil {
		t.Fatal("Apply accepted a row set the resource refuses")
	}
	if !strings.Contains(err.Error(), "validatedrows: validate") {
		t.Errorf("err = %v, want it to name the resource and the check", err)
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]DefaultedRow)
	if len(rows) != 1 || rows[0].Key != "kept" {
		t.Errorf("rows = %v, want the pre-apply set untouched; the check ran after the "+
			"wipe, which on this path is a delete nothing undoes", rows)
	}
}

// --- the declared codec -------------------------------------------------
//
// Every attribute value is a string, so proxyd_routes' `preserve_headers
// []string` and `strip_prefix bool` do not survive as items without a rendering.
// The claim the bodies below hold is not "it round-trips" but "the stored TEXT
// is what the codec rendered": one renderer, so a migration carrying a table
// into items cannot change a value on the way (spec 6/40 §"What a registered
// resource still needs", answer 3).

// CodecRow is proxyd_routes' two awkward fields on their own columns — no
// shadow column, no Hooks — plus a string to anchor the row.
type CodecRow struct {
	Path            string   `db:"path"                  yaml:"path"`
	PreserveHeaders []string `db:"preserve_headers,json" yaml:"preserve_headers,omitempty"`
	StripPrefix     bool     `db:"strip_prefix,bool"     yaml:"strip_prefix,omitempty"`
}

// codecEntitySubsystem is the synthetic owner tag the codec'd resource is
// registered under, and the partition its items live in.
const codecEntitySubsystem = "codecobj"

// freshCodecEntity registers the entity half alone. It is engine-neutral, so
// every body that only reads or writes ITEMS runs on both drivers.
func freshCodecEntity(t *testing.T) (objstore.Store, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	obj := Register(Resource{
		Name:          "codecrows_obj",
		Table:         "codecitems",
		DB:            codecEntitySubsystem,
		RowType:       reflect.TypeFor[CodecRow](),
		PKFields:      []string{"Path"},
		EntitySortKey: []string{"Path"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, codecEntitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return s, obj
}

// codecRows are the values that have to survive: a populated list, an empty
// one, and both settings of the flag.
var codecRows = []CodecRow{
	{Path: "/a", PreserveHeaders: []string{"X-Api-Key", "Authorization"}, StripPrefix: true},
	{Path: "/b", PreserveHeaders: []string{}, StripPrefix: false},
	{Path: "/c", StripPrefix: true},
}

// The stored text is what the codec rendered, and the value comes back through
// the reader unchanged. One renderer is what lets a second reader of the same
// value — proxyd's own start-up read of a route — agree with the engine about
// what the value IS (codec.go).
func TestCodec_ItemsHoldTheRenderedText(t *testing.T) {
	s, obj := freshCodecEntity(t)
	be := On(s)
	if _, err := Apply(t.Context(), be, codecEntitySubsystem, "", true,
		map[string]any{"codecrows_obj": codecRows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	fromItems, err := obj.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll items: %v", err)
	}
	want := []CodecRow{
		{Path: "/a", PreserveHeaders: []string{"X-Api-Key", "Authorization"}, StripPrefix: true},
		{Path: "/b", PreserveHeaders: []string{}, StripPrefix: false},
		{Path: "/c", PreserveHeaders: []string{}, StripPrefix: true},
	}
	if !reflect.DeepEqual(fromItems, want) {
		t.Errorf("read back %#v, want %#v", fromItems, want)
	}

	// The stored text, read past the engine rather than through it. /c carries a
	// NIL list, and it is stored as the EMPTY DOCUMENT rather than as JSON null:
	// proxyd_routes' value is json.Unmarshalled by store.ProxydRoutes on every
	// proxyd start, so `null` is a value a second reader cannot use.
	items, err := s.Query(t.Context(), objstore.Query{
		Entity: "codecitems", Partition: codecEntitySubsystem})
	if err != nil {
		t.Fatalf("query items: %v", err)
	}
	if len(items) != len(codecRows) {
		t.Fatalf("stored %d items, want %d", len(items), len(codecRows))
	}
	for _, it := range items {
		if it.Key.Sort == "/c" && it.Attrs["preserve_headers"] != "[]" {
			t.Errorf("a nil list stored as the attribute %q, want %q",
				it.Attrs["preserve_headers"], "[]")
		}
	}
}

// Stored text that is not the value it claims to be is a LOUD fault on the
// object path, not a zero: a row read wrong goes into an export the operator
// then applies.
func TestCodec_RefusesTextItCannotRead(t *testing.T) {
	s, obj := freshCodecEntity(t)
	for _, tc := range []struct {
		name  string
		attrs map[string]string
		want  string
	}{
		{"not json", map[string]string{"preserve_headers": "X-Api-Key"},
			"is not the JSON this field holds"},
		{"not a bool", map[string]string{"preserve_headers": "[]", "strip_prefix": "yes"},
			"is not a bool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
				Op: objstore.Put, Entity: "codecitems",
				Key:   objstore.Key{Partition: codecEntitySubsystem, Sort: "/x"},
				Attrs: tc.attrs,
			}})
			if err != nil || !committed {
				t.Fatalf("put: committed=%v err=%v", committed, err)
			}
			_, err = obj.ScanAll(On(s))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ScanAll err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The declaration's own refusals. The set of encodings is CLOSED, and a string
// or an int may not declare one: both are already rendered, and a second
// rendering of a value that has one is the drift the codec exists to prevent.
func TestCodec_RegisterRefusesAMisdeclaredEncoding(t *testing.T) {
	type badJSON struct {
		Key  string `db:"key"`
		Note string `db:"note,nope"`
	}
	type stringCodec struct {
		Key  string `db:"key"`
		Note string `db:"note,json"`
	}
	type intCodec struct {
		Key   string `db:"key"`
		Count int    `db:"count,json"`
	}
	type boolOnASlice struct {
		Key  string   `db:"key"`
		List []string `db:"list,bool"`
	}
	cases := []struct {
		name string
		want string
		row  reflect.Type
	}{
		{"unknown encoding", "the set is json and bool", reflect.TypeFor[badJSON]()},
		{"encoding on a string", "already render a string and an int", reflect.TypeFor[stringCodec]()},
		{"encoding on an int", "already render a string and an int", reflect.TypeFor[intCodec]()},
		{"bool on a slice", "declares the bool encoding", reflect.TypeFor[boolOnASlice]()},
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
			Register(Resource{
				Name: "bad", Table: "bad", DB: entitySubsystem,
				RowType: tc.row, PKFields: []string{"Key"},
			})
		})
	}
}

// --- the scoped delete, inside the replace ------------------------------
//
// Five of the six simplest routd resources sit in store.folderScopePurge or
// claim a cascade off `groups`, and an entity has neither a table to delete
// from nor a foreign key (BUGS L253). A folder purge becomes a Query on the
// scope plus guarded deletes, and for Apply those deletes ride INSIDE the
// replace's claimed scope rather than beside it (spec 6/40 §"What a registered
// resource still needs", answer 4).

// ScopedRow scopes on an ATTRIBUTE: web_routes' shape, keyed on path_prefix
// with the folder beside it.
type ScopedRow struct {
	Path   string `db:"path"   yaml:"path"`
	Folder string `db:"folder" yaml:"folder"`
	Access string `db:"access" yaml:"access,omitempty"`
}

// KeyScopedRow scopes on the WHOLE coordinate: groups' shape, where the folder
// is the key.
type KeyScopedRow struct {
	Folder  string `db:"folder"  yaml:"folder"`
	Product string `db:"product" yaml:"product,omitempty"`
}

// PrefixScopedRow scopes on the FIRST field of a composite coordinate:
// network_rules' (folder, target).
type PrefixScopedRow struct {
	Folder string `db:"folder" yaml:"folder"`
	Target string `db:"target" yaml:"target"`
}

// registerScoped registers one entity-backed scoped resource over a fresh
// database and returns it ready to apply.
func registerScoped(t *testing.T, r Resource) (Backend, *Resource) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	out := Register(r)
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), out
}

// A scoped apply takes the MENTIONED folders' items and leaves every other
// folder's alone, over all three shapes a scope can have.
func TestEntity_ScopedApplyTouchesOnlyTheMentionedScopes(t *testing.T) {
	t.Run("scope is an attribute", func(t *testing.T) {
		be, r := registerScoped(t, Resource{
			Name: "scopedrows", Table: "scopedrows", DB: entitySubsystem,
			RowType: reflect.TypeFor[ScopedRow](), PKFields: []string{"Path"},
			EntitySortKey: []string{"Path"}, Scope: ScopeSpec{Field: "Folder"},
		})
		all := []ScopedRow{
			{Path: "/a", Folder: "corp/eng", Access: "public"},
			{Path: "/b", Folder: "corp/sre", Access: "auth"},
		}
		applyRows(t, be, r, all)
		// A manifest naming ONE folder, with a different row under it.
		applyRows(t, be, r, []ScopedRow{{Path: "/c", Folder: "corp/eng", Access: "deny"}})
		got := scanRows[ScopedRow](t, be, r)
		want := []ScopedRow{
			{Path: "/b", Folder: "corp/sre", Access: "auth"},
			{Path: "/c", Folder: "corp/eng", Access: "deny"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rows = %v, want %v — corp/sre was never mentioned", got, want)
		}
	})

	t.Run("scope is an absent attribute", func(t *testing.T) {
		be, r := registerScoped(t, Resource{
			Name: "scopedrows", Table: "scopedrows", DB: entitySubsystem,
			RowType: reflect.TypeFor[ScopedRow](), PKFields: []string{"Path"},
			EntitySortKey: []string{"Path"}, Scope: ScopeSpec{Field: "Folder"},
		})
		applyRows(t, be, r, []ScopedRow{
			{Path: "/a", Folder: "", Access: "public"},
			{Path: "/b", Folder: "corp/eng", Access: "auth"},
		})
		applyRows(t, be, r, []ScopedRow{{Path: "/c", Folder: "", Access: "deny"}})
		got := scanRows[ScopedRow](t, be, r)
		want := []ScopedRow{
			{Path: "/b", Folder: "corp/eng", Access: "auth"},
			{Path: "/c", Folder: "", Access: "deny"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rows = %v, want %v — an empty scope is an ABSENT attribute, so "+
				"Eq \"\" would select nothing and leave /a behind", got, want)
		}
	})

	t.Run("scope is the coordinate", func(t *testing.T) {
		be, r := registerScoped(t, Resource{
			Name: "keyscopedrows", Table: "keyscopedrows", DB: entitySubsystem,
			RowType: reflect.TypeFor[KeyScopedRow](), PKFields: []string{"Folder"},
			EntitySortKey: []string{"Folder"}, Scope: ScopeSpec{Field: "Folder"},
		})
		applyRows(t, be, r, []KeyScopedRow{
			{Folder: "corp/eng", Product: "one"},
			{Folder: "corp/sre", Product: "two"},
		})
		applyRows(t, be, r, []KeyScopedRow{{Folder: "corp/eng", Product: "three"}})
		got := scanRows[KeyScopedRow](t, be, r)
		want := []KeyScopedRow{
			{Folder: "corp/eng", Product: "three"},
			{Folder: "corp/sre", Product: "two"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rows = %v, want %v", got, want)
		}
	})

	t.Run("scope is the first field of a composite coordinate", func(t *testing.T) {
		be, r := registerScoped(t, Resource{
			Name: "prefixscopedrows", Table: "prefixscopedrows", DB: entitySubsystem,
			RowType: reflect.TypeFor[PrefixScopedRow](), PKFields: []string{"Folder", "Target"},
			EntitySortKey: []string{"Folder", "Target"}, EntityKeySep: compositeSep,
			Scope: ScopeSpec{Field: "Folder"},
		})
		applyRows(t, be, r, []PrefixScopedRow{
			{Folder: "corp/eng", Target: "a.example.com"},
			{Folder: "corp/eng", Target: "b.example.com"},
			{Folder: "corp/engineering", Target: "c.example.com"},
			{Folder: "", Target: "base.example.com"},
		})
		applyRows(t, be, r, []PrefixScopedRow{{Folder: "corp/eng", Target: "d.example.com"}})
		got := scanRows[PrefixScopedRow](t, be, r)
		want := []PrefixScopedRow{
			{Folder: "", Target: "base.example.com"},
			{Folder: "corp/eng", Target: "d.example.com"},
			{Folder: "corp/engineering", Target: "c.example.com"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("rows = %v, want %v — the prefix is scope+separator, so corp/eng "+
				"does not reach corp/engineering and the instance-wide row stays", got, want)
		}
	})
}

// A folder the manifest does NOT mention is purged when the caller names it in
// PruneScopes: the cross-subsystem rollback's reach, and the thing that stops a
// folder a forward apply created from outliving the rollback (ApplyOpts).
func TestEntity_ScopedApplyPrunesTheScopesTheCallerNames(t *testing.T) {
	be, r := registerScoped(t, Resource{
		Name: "scopedrows", Table: "scopedrows", DB: entitySubsystem,
		RowType: reflect.TypeFor[ScopedRow](), PKFields: []string{"Path"},
		EntitySortKey: []string{"Path"}, Scope: ScopeSpec{Field: "Folder"},
	})
	applyRows(t, be, r, []ScopedRow{
		{Path: "/a", Folder: "corp/eng", Access: "public"},
		{Path: "/b", Folder: "corp/gone", Access: "public"},
	})
	rows := []ScopedRow{{Path: "/a", Folder: "corp/eng", Access: "auth"}}
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{"scopedrows": rows},
		&ApplyOpts{Actor: "test", PruneScopes: []string{"corp/gone"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := scanRows[ScopedRow](t, be, r)
	want := []ScopedRow{{Path: "/a", Folder: "corp/eng", Access: "auth"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v — corp/gone is named in PruneScopes, and a row "+
			"left there is authority the next tenant of the path inherits", got, want)
	}
}

// A scoped resource the manifest mentions with an EMPTY list still wipes the
// whole entity: the operator is declaring "no rows for this resource".
func TestEntity_ScopedApplyWipesOnAnEmptyList(t *testing.T) {
	be, r := registerScoped(t, Resource{
		Name: "scopedrows", Table: "scopedrows", DB: entitySubsystem,
		RowType: reflect.TypeFor[ScopedRow](), PKFields: []string{"Path"},
		EntitySortKey: []string{"Path"}, Scope: ScopeSpec{Field: "Folder"},
	})
	applyRows(t, be, r, []ScopedRow{
		{Path: "/a", Folder: "corp/eng"},
		{Path: "/b", Folder: "corp/sre"},
	})
	applyRows(t, be, r, []ScopedRow{})
	if got := scanRows[ScopedRow](t, be, r); len(got) != 0 {
		t.Errorf("rows = %v, want none", got)
	}
}

// A scope sitting INSIDE a coordinate rather than at the front of it has no
// predicate: a key operator reaches a leading fragment, and the coordinate is
// not an attribute a Where can filter on.
func TestEntity_RegisterRefusesAScopeInsideACoordinate(t *testing.T) {
	reset()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("Register accepted a scope on the second field of a coordinate")
		}
		if msg, _ := got.(string); !strings.Contains(msg, "has to be the first field of it") {
			t.Errorf("panic = %v, want it to name the leading-fragment rule", got)
		}
	}()
	Register(Resource{
		Name: "bad", Table: "bad", DB: entitySubsystem,
		RowType: reflect.TypeFor[PrefixScopedRow](), PKFields: []string{"Folder", "Target"},
		EntitySortKey: []string{"Folder", "Target"}, EntityKeySep: compositeSep,
		Scope: ScopeSpec{Field: "Target"},
	})
}

// applyRows applies one resource's rows and fails the test on any error.
func applyRows(t *testing.T, be Backend, r *Resource, rows any) {
	t.Helper()
	if _, err := Apply(t.Context(), be, entitySubsystem, "", true,
		map[string]any{r.Name: rows}, nil); err != nil {
		t.Fatalf("Apply %s: %v", r.Name, err)
	}
}

// scanRows reads one resource's rows back, in sort-key order.
func scanRows[T any](t *testing.T, be Backend, r *Resource) []T {
	t.Helper()
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll %s: %v", r.Name, err)
	}
	return got.([]T)
}

// The scoped deletes ride INSIDE the replace's claim, not beside it.
//
// That is the whole of answer 4: the scope's items are READ and then deleted —
// an entity has no predicate delete — so between the read and the write there
// is a window, and a second replace taking the scope in that window must stop
// these deletes rather than let them land on a projection somebody else is
// part-way through replacing. conformance.SlipAt forces the interleave instead
// of racing for it: two concurrency bodies in this project stayed green against
// deliberately broken code because the collision has to fall in one window and
// usually does not.
func TestEntity_ScopedDeletesRideInsideTheClaim(t *testing.T) {
	reset()
	s := dyntest.Store(t)
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("audit_log: %v", err)
	}
	r := Register(Resource{
		Name: "scopedrows", Table: "scopedrows", DB: entitySubsystem,
		RowType: reflect.TypeFor[ScopedRow](), PKFields: []string{"Path"},
		EntitySortKey: []string{"Path"}, Scope: ScopeSpec{Field: "Folder"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	before := []ScopedRow{
		{Path: "/a", Folder: "corp/eng", Access: "public"},
		{Path: "/b", Folder: "corp/sre", Access: "auth"},
	}
	applyRows(t, On(s), r, before)

	// Read 1 is the checksum's scan; read 2 is the scope query whose answer the
	// deletes are built from. Steal the claim at read 2, which is the window.
	reads := 0
	slipped := conformance.SlipAt(s, "scopedrows", 2, func() {
		reads++
		if reads != 2 {
			return
		}
		committed, err := s.GuardedBatch(t.Context(), nil, []objstore.Write{{
			Op: objstore.Update, Entity: store.ScopeEntity,
			Key:   store.OwnerScope(entitySubsystem).Key,
			Attrs: map[string]string{"holder": "somebody-else", "since": core.NowTS()},
		}})
		if err != nil || !committed {
			t.Errorf("steal the claim: committed=%v err=%v", committed, err)
		}
	})

	_, err := Apply(t.Context(), On(slipped), entitySubsystem, "", true,
		map[string]any{"scopedrows": []ScopedRow{{Path: "/c", Folder: "corp/eng"}}}, nil)
	if !errors.Is(err, objstore.ErrClaimLost) {
		t.Fatalf("Apply err = %v, want ErrClaimLost: the deletes were built from a read "+
			"taken under a claim this apply no longer holds", err)
	}
	if reads < 2 {
		t.Fatalf("the scope query never ran (%d reads), so the interleave proved nothing", reads)
	}
	if got := scanRows[ScopedRow](t, On(s), r); !reflect.DeepEqual(got, before) {
		t.Errorf("rows = %v, want %v — a fenced delete writes nothing", got, before)
	}
}

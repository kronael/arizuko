package main

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// BUGS F49 / spec 5/8 §"Round-trip honesty": `export` must emit something that
// re-applies to a no-op, and the content-hash CAS depends on it. It does not
// hold for a row whose server-owned instant is ABSENT unless the writer knows
// not to invent one.
//
// `groups.updated_at` is when the group was last changed, and a group nobody
// has changed carries none. `groups.added_at` is when it was made, and every
// group has one. Both are in StampedFields — server-OWNED — and only the second
// is server-INVENTED, which is what AbsentStamps declares. Without that, the
// first `export | apply` writes a fresh now() into the first and the operator
// gets a checksum shift they did not cause.

// updatedAtOf answers whether the group item carries an updated_at attribute at
// all. Absent is the only null this model has, so "is it there" is the whole
// question.
func updatedAtOf(t *testing.T, st *store.Store, folder string) (string, bool) {
	t.Helper()
	items, err := st.Obj().Query(context.Background(), objstore.Query{
		Entity: store.Groups, Partition: store.OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: folder},
	})
	if err != nil {
		t.Fatalf("read the group item: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("group %q has no item", folder)
	}
	v, ok := items[0].Attrs["updated_at"]
	return v, ok
}

// TestExportApplyIsNoOpForNullStampedColumn: pass ZERO must not move the
// checksum, and must leave the absent instant absent.
func TestExportApplyIsNoOpForNullStampedColumn(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]

	// A group carrying added_at and no updated_at. No live writer makes one:
	// every group-writing method stamps both, so the fixture is seeded.
	if err := st.SeedItem(context.Background(), store.Groups, []string{"acme"},
		map[string]string{
			"added_at": core.TS(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
			"product":  "assistant",
		}); err != nil {
		t.Fatalf("seed the group: %v", err)
	}
	if _, ok := updatedAtOf(t, st, "acme"); ok {
		t.Fatal("fixture is vacuous: the seeded group already carries an updated_at")
	}

	before, err := resreg.Checksum(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}

	// The operator's honest workflow: export, review, apply unmodified.
	exp, err := resreg.Export(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	yamlBytes, err := resreg.EmitYAML(exp)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := resreg.ParseYAML(yamlBytes)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	after, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, before, false, parsed, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after != before {
		t.Errorf("first export|apply moved the checksum with nothing changed: %s -> %s", before, after)
	}

	// The absence survived rather than becoming now(), which is what the
	// resource's AbsentStamps declares.
	if v, ok := updatedAtOf(t, st, "acme"); ok {
		t.Errorf("re-apply invented an updated_at: %q, want the attribute still absent", v)
	}
}

// TestApplyStillStampsAFieldWithNoWriteHook is the converse guard: the
// declaration narrows the auto-stamp, it must not disable it. `groups.added_at`
// is not in AbsentStamps, so a hand-written manifest that omits it still gets a
// server timestamp rather than nothing.
func TestApplyStillStampsAFieldWithNoWriteHook(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]

	sum, err := resreg.Checksum(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, sum, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "acme", Product: "assistant"}},
	}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	g, ok := st.GroupByFolder("acme")
	if !ok {
		t.Fatal("the applied group has no item")
	}
	if addedAt := core.TS(g.AddedAt); g.AddedAt.IsZero() || addedAt == "" {
		t.Error("added_at was not auto-stamped: the fix disabled the stamp instead of narrowing it")
	}
}

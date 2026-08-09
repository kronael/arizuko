package main

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// BUGS F49 / spec 5/8 §"Round-trip honesty": `export` must emit something that
// re-applies to a no-op, and the content-hash CAS depends on it. It did not
// hold on the FIRST pass for a row whose server-stamped column was NULL.
//
// `groups.updated_at` is nullable, has no default, and is in StampedFields with
// a `nilIfEmptyString` write hook — so a NULL exports as `updated_at: ""`, and
// re-inserting that unmodified row used to hit Insert's auto-stamp, which read
// "" as "unset, invent one" and wrote a fresh now(). The operator got a
// checksum shift they did not cause, and 5/8's cross-subsystem rollback
// guarantee held only once every stamped column was non-NULL.

// TestExportApplyIsNoOpForNullStampedColumn: pass ZERO must not move the
// checksum, and must leave the NULL a NULL.
func TestExportApplyIsNoOpForNullStampedColumn(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]

	// A group written the way store.RegisterGroup writes one: added_at set,
	// updated_at never touched. This is the NORMAL state, not a corrupt row.
	if _, err := st.DB().Exec(
		`INSERT INTO groups (folder, added_at, product) VALUES ('acme', '2026-08-01T00:00:00Z', 'assistant')`,
	); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM groups WHERE updated_at IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatalf("fixture is vacuous: %d rows with a NULL updated_at, want 1", nulls)
	}

	before, err := resreg.Checksum(st.DB(), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}

	// The operator's honest workflow: export, review, apply unmodified.
	exp, err := resreg.Export(st.DB(), resreg.SubsystemRoutd)
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
	after, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, before, false, parsed, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after != before {
		t.Errorf("first export|apply moved the checksum with nothing changed: %s -> %s", before, after)
	}

	// The NULL survived rather than becoming now(): "" round-trips to NULL,
	// which is exactly what the resource's empty→NULL write hook declares.
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM groups WHERE updated_at IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Errorf("re-apply stamped the NULL updated_at: %d NULL rows, want 1", nulls)
	}
}

// TestApplyStillStampsAFieldWithNoWriteHook is the converse guard: the fix
// narrows the auto-stamp, it must not disable it. `groups.added_at` is NOT NULL
// with no write hook, so a hand-written manifest that omits it still gets a
// server timestamp rather than an empty string.
func TestApplyStillStampsAFieldWithNoWriteHook(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]

	sum, err := resreg.Checksum(st.DB(), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, sum, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "acme", Product: "assistant"}},
	}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var addedAt string
	if err := st.DB().QueryRow(
		`SELECT COALESCE(added_at,'') FROM groups WHERE folder = 'acme'`).Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt == "" {
		t.Error("added_at was not auto-stamped: the fix disabled the stamp instead of narrowing it")
	}
}

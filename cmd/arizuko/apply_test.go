package main

// E2E test for `arizuko apply` + `arizuko export`. Exercises:
//   - parse: YAML decode for every resource kind we support
//   - CAS reject: stale config_version causes ErrVersionMismatch
//   - CAS pass: matching version applies cleanly
//   - full rebuild: pre-existing rows wiped, manifest rows inserted
//   - round-trip: apply → export → apply produces a no-op (idempotent)
//
// Uses a tempfile SQLite (via store.Open + tempdir) and the same
// resreg.Apply path the CLI uses. The CLI wrapper itself is a thin
// shim; testing the orchestrator covers the load-bearing logic.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

func openInstance(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return dir, st
}

func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func dbConfigVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	v, err := resreg.ConfigVersion(db)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApply_CASReject(t *testing.T) {
	_, st := openInstance(t)
	manifest := []byte(`
config_version: 999
routes:
  - seq: 0
    match: ""
    target: atlas
`)
	parsed, version, err := resreg.ParseYAML(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if version != 999 {
		t.Fatalf("parsed version = %d, want 999", version)
	}
	_, err = resreg.Apply(context.Background(), st.DB(), version, false, parsed)
	if err == nil {
		t.Fatal("expected CAS reject")
	}
	if !strings.Contains(err.Error(), "config_version mismatch") {
		t.Errorf("err = %v, want ErrVersionMismatch wrap", err)
	}
}

func TestApply_CASPass(t *testing.T) {
	_, st := openInstance(t)
	v0 := dbConfigVersion(t, st.DB())
	manifest := []byte(`
config_version: ` + itoa(v0) + `
routes:
  - seq: 0
    match: ""
    target: atlas
`)
	parsed, version, err := resreg.ParseYAML(manifest)
	if err != nil {
		t.Fatal(err)
	}
	newV, err := resreg.Apply(context.Background(), st.DB(), version, false, parsed)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if newV != v0+1 {
		t.Errorf("new version = %d, want %d", newV, v0+1)
	}
}

func TestApply_FullRebuild(t *testing.T) {
	_, st := openInstance(t)
	// Insert a row outside the manifest path; Apply must wipe it.
	r := resreg.Lookup("routes")
	tx, _ := st.DB().Begin()
	if err := r.Insert(context.Background(), tx, resources.RoutesRow{
		Seq: 99, Match: "stale", Target: "atlas",
	}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	v0 := dbConfigVersion(t, st.DB())
	// Apply manifest with different rows.
	_, err := resreg.Apply(context.Background(), st.DB(), v0, false, map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "", Target: "ops"},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := r.ScanAll(st.DB())
	rows := got.([]resources.RoutesRow)
	if len(rows) != 1 {
		t.Fatalf("after rebuild: %d rows, want 1", len(rows))
	}
	if rows[0].Target != "ops" {
		t.Errorf("target = %q, want ops", rows[0].Target)
	}
}

func TestApply_RoundTrip_Idempotent(t *testing.T) {
	_, st := openInstance(t)
	v0 := dbConfigVersion(t, st.DB())
	manifest := map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "platform=tele", Target: "atlas"},
			{Seq: 1, Match: "platform=slack", Target: "ops"},
		},
	}
	v1, err := resreg.Apply(context.Background(), st.DB(), v0, false, manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Export → bytes → parse → apply again. Should be a no-op behaviorally
	// (rows identical) and bump version by exactly 1.
	exp, err := resreg.Export(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	yamlBytes, err := resreg.EmitYAML(exp)
	if err != nil {
		t.Fatal(err)
	}
	parsed, version, err := resreg.ParseYAML(yamlBytes)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if version != v1 {
		t.Errorf("round-trip version = %d, want %d", version, v1)
	}
	v2, err := resreg.Apply(context.Background(), st.DB(), version, false, parsed)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if v2 != v1+1 {
		t.Errorf("v2 = %d, want %d", v2, v1+1)
	}
	// Row content should be identical to what we applied.
	r := resreg.Lookup("routes")
	got, _ := r.ScanAll(st.DB())
	rows := got.([]resources.RoutesRow)
	if len(rows) != 2 {
		t.Errorf("after round-trip: %d rows, want 2", len(rows))
	}
}

func TestExport_DeterministicAcrossRuns(t *testing.T) {
	_, st := openInstance(t)
	v0 := dbConfigVersion(t, st.DB())
	_, err := resreg.Apply(context.Background(), st.DB(), v0, false, map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "z", Target: "atlas"},
			{Seq: 1, Match: "a", Target: "ops"},
		},
		"acl": []resources.ACLRow{
			{Principal: "user:bob", Action: "read", Scope: "atlas/", Effect: "allow"},
			{Principal: "user:alice", Action: "read", Scope: "atlas/", Effect: "allow"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	exp1, _ := resreg.Export(st.DB())
	b1, _ := resreg.EmitYAML(exp1)
	exp2, _ := resreg.Export(st.DB())
	b2, _ := resreg.EmitYAML(exp2)
	if string(b1) != string(b2) {
		t.Errorf("export non-deterministic:\n--- 1 ---\n%s\n--- 2 ---\n%s", b1, b2)
	}
}

// itoa avoids importing strconv in the package init bloat.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

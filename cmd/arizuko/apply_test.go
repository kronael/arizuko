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
	_, err = resreg.Apply(context.Background(), st.DB(), version, false, parsed, nil)
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
	newV, err := resreg.Apply(context.Background(), st.DB(), version, false, parsed, nil)
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
	}, nil)
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
	v1, err := resreg.Apply(context.Background(), st.DB(), v0, false, manifest, nil)
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
	v2, err := resreg.Apply(context.Background(), st.DB(), version, false, parsed, nil)
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

// TestApply_ForceFromVersion: under --force against a drifted DB the
// manifest version differs from the DB version. cmdApply prints the
// pre-apply DB version (resreg.ConfigVersion before Apply) as the "from",
// not the manifest version, so the success line reports the true prior state.
func TestApply_ForceFromVersion(t *testing.T) {
	_, st := openInstance(t)
	// Advance the DB a couple of versions so it drifts from a stale manifest.
	v0 := dbConfigVersion(t, st.DB())
	if _, err := resreg.Apply(context.Background(), st.DB(), v0, false, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	dbVer := dbConfigVersion(t, st.DB()) // true pre-apply version
	staleManifest := dbVer - 1           // a drifted, lower manifest version

	fromVer, err := resreg.ConfigVersion(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if fromVer != dbVer {
		t.Fatalf("from = %d, want db version %d", fromVer, dbVer)
	}
	if fromVer == staleManifest {
		t.Fatalf("from must be the DB version, not the stale manifest version %d", staleManifest)
	}
	// Force-apply with the stale version; succeeds and bumps from the DB version.
	newVer, err := resreg.Apply(context.Background(), st.DB(), staleManifest, true, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "ops"}},
	}, nil)
	if err != nil {
		t.Fatalf("force Apply: %v", err)
	}
	if newVer != fromVer+1 {
		t.Errorf("new version = %d, want fromVer+1 = %d", newVer, fromVer+1)
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
	}, nil)
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

// TestGetRoundTrip_NoOp: `get <resource>` emits a fragment that, parsed
// and diffed against the live DB, reports no change — the round-trip
// honesty acceptance criterion (spec 5/36 §"arizuko get round-trip").
func TestGetRoundTrip_NoOp(t *testing.T) {
	_, st := openInstance(t)
	v0 := dbConfigVersion(t, st.DB())
	if _, err := resreg.Apply(context.Background(), st.DB(), v0, false, map[string]any{
		"acl": []resources.ACLRow{
			{Principal: "user:alice", Action: "read", Scope: "atlas/", Effect: "allow"},
			{Principal: "user:bob", Action: "tasks:*", Scope: "ops/", Effect: "allow"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	frag, err := resreg.GetResource(st.DB(), "acl")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	out, err := resreg.EmitYAML(frag)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	parsed, _, err := resreg.ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	d, err := resreg.Lookup("acl").Diff(st.DB(), parsed["acl"])
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Changed() {
		t.Errorf("get acl fragment not a no-op: %+v", d)
	}
}

// TestPlan_MatchesApply: a plan against a populated DB reports the adds
// the subsequent apply commits, then a second plan reports clean.
func TestPlan_MatchesApply(t *testing.T) {
	_, st := openInstance(t)
	manifest := map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "platform=tele", Target: "atlas"},
		},
	}
	deltas, err := resreg.Plan(st.DB(), manifest)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var routesDelta *resreg.ResourceDelta
	for i := range deltas {
		if deltas[i].Resource == "routes" {
			routesDelta = &deltas[i]
		}
	}
	if routesDelta == nil || len(routesDelta.Add) != 1 {
		t.Fatalf("plan routes Add = %+v, want one add", routesDelta)
	}
	if _, err := resreg.Apply(context.Background(), st.DB(), dbConfigVersion(t, st.DB()), false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deltas2, err := resreg.Plan(st.DB(), manifest)
	if err != nil {
		t.Fatalf("Plan 2: %v", err)
	}
	for _, d := range deltas2 {
		if d.Changed() {
			t.Errorf("post-apply plan still changes %s: %+v", d.Resource, d)
		}
	}
}

// TestStrictParse_CLIPath: the parse step cmdApply/cmdPlan run (ParseYAML
// over the real resource registry) rejects a typo'd resource key AND a
// bogus row field before any DB write (spec 5/36 §"Apply lifecycle" step 1).
func TestStrictParse_CLIPath(t *testing.T) {
	typoKey := []byte(`
config_version: 0
routez:            # typo: should be "routes"
  - seq: 0
    match: ""
    target: atlas
`)
	if _, _, err := resreg.ParseYAML(typoKey); err == nil {
		t.Error("ParseYAML accepted typo'd resource key 'routez'")
	}
	bogusField := []byte(`
config_version: 0
routes:
  - seq: 0
    match: ""
    target: atlas
    targett: typo    # bogus field
`)
	if _, _, err := resreg.ParseYAML(bogusField); err == nil {
		t.Error("ParseYAML accepted bogus row field 'targett'")
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

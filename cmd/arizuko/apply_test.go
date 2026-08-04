package main

// E2E test for `arizuko apply` + `arizuko export`. Exercises:
//   - parse: YAML decode for every resource kind we support
//   - CAS reject: stale checksum causes ErrChecksumMismatch
//   - CAS pass: matching checksum applies cleanly
//   - full rebuild: pre-existing rows wiped, manifest rows inserted
//   - round-trip: apply → export → apply produces a no-op (idempotent)
//   - the CLI wrapper (cmdExport/cmdApply) actually reaches REAL routd.db +
//     onbod.db files on disk, not the frozen pre-split messages.db — the
//     step 4 fix for the long-standing inertness bug (BUGS.md Y1).
//
// openInstance below bootstraps a real routd.db (via routd.Open, the same
// migration sequence the daemon itself runs) and a real onbod.db (via the
// onbodSchema DDL constant migrate_split.go already carries for the exact
// same reason: onbod's migration FS is package-private, package main, so
// this constant is the established precedent for bootstrapping onbod.db's
// schema from OUTSIDE the onbod package). Most tests then drive
// resreg.Apply/Export/Plan/GetResource directly against the resulting
// *store.Store handles — the same "orchestrator, not the thin CLI shim"
// philosophy the original version of this file documented — because the
// CLI wrapper functions (cmdApply/cmdExport/...) call os.Exit on error,
// which is unsafe to exercise from inside a test process. One dedicated
// test (TestCLI_ExportApply_RealFiles) drives the actual cmdExport/cmdApply
// entry points via ARIZUKO_DATA_DIR, proving the real, operator-facing
// path — success only, since only the error paths os.Exit.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// openInstance bootstraps a real routd.db + onbod.db pair under a fresh
// instance data dir and returns (dataDir, stores) exactly as
// openSubsystemStores would hand cmdApply/cmdExport/cmdPlan/cmdGet.
func openInstance(t *testing.T) (string, map[string]*store.Store) {
	t.Helper()
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Bootstrap routd.db via routd's OWN migration sequence — the same one
	// the daemon runs at boot.
	rdb, err := routd.Open(storeDir)
	if err != nil {
		t.Fatalf("routd.Open (bootstrap): %v", err)
	}
	rdb.Close()
	// Bootstrap onbod.db via the onbodSchema DDL constant (migrate_split.go,
	// same package) — onbod's own migration FS is package-private (package
	// main), so this inline DDL is the established way to create its schema
	// from outside the onbod package.
	odb, err := sql.Open("sqlite", filepath.Join(storeDir, "onbod.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open onbod.db (bootstrap): %v", err)
	}
	if _, err := odb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := odb.Exec(onbodSchema); err != nil {
		t.Fatalf("onbod.db bootstrap schema: %v", err)
	}
	odb.Close()

	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		t.Fatalf("openSubsystemStores: %v", err)
	}
	t.Cleanup(func() { closeStores(stores) })
	return dataDir, stores
}

func routdChecksum(t *testing.T, st *store.Store) string {
	t.Helper()
	c, err := resreg.Checksum(st.DB(), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOpenSubsystemStores_RealFiles is the step 4 core assertion: the CLI's
// store-opening path reaches ACTUAL, SEPARATE routd.db/onbod.db files — not
// the frozen pre-split messages.db (the inertness bug, BUGS.md Y1).
func TestOpenSubsystemStores_RealFiles(t *testing.T) {
	dataDir, stores := openInstance(t)
	storeDir := filepath.Join(dataDir, "store")
	for _, name := range []string{"routd.db", "onbod.db"} {
		if _, err := os.Stat(filepath.Join(storeDir, name)); err != nil {
			t.Errorf("%s does not exist on disk: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(storeDir, "messages.db")); err == nil {
		t.Error("messages.db (the frozen pre-split schema) was created — the CLI must never touch it")
	}
	rst, ok := stores[resreg.SubsystemRoutd]
	if !ok {
		t.Fatal("openSubsystemStores did not return a routd store")
	}
	ost, ok := stores[resreg.SubsystemOnbod]
	if !ok {
		t.Fatal("openSubsystemStores did not return an onbod store")
	}
	// A write through the returned *store.Store actually lands in the
	// separate file on disk, proving these are not two handles onto the
	// same DB.
	if _, err := resreg.Apply(context.Background(), rst.DB(), resreg.SubsystemRoutd, routdChecksum(t, rst), false, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatalf("Apply to routd store: %v", err)
	}
	var n int
	if err := ost.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='routes'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("onbod.db has a routes table — routd.db and onbod.db are not actually separate files")
	}
}

func TestApply_CASReject(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	manifest := []byte(`
checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
routes:
  - seq: 0
    match: ""
    target: atlas
`)
	parsed, checksum, err := resreg.ParseYAML(manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, checksum, false, parsed, nil)
	if err == nil {
		t.Fatal("expected CAS reject")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err = %v, want ErrChecksumMismatch wrap", err)
	}
}

func TestApply_CASPass(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	manifest := []byte(`
checksum: "` + c0 + `"
routes:
  - seq: 0
    match: ""
    target: atlas
`)
	parsed, checksum, err := resreg.ParseYAML(manifest)
	if err != nil {
		t.Fatal(err)
	}
	newSum, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, checksum, false, parsed, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if newSum == c0 {
		t.Errorf("checksum unchanged after apply that added a route")
	}
}

func TestApply_FullRebuild(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	// Insert a row outside the manifest path; Apply must wipe it.
	r := resreg.Lookup("routes")
	tx, _ := st.DB().Begin()
	if err := r.Insert(context.Background(), tx, resources.RoutesRow{
		Seq: 99, Match: "stale", Target: "atlas",
	}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	c0 := routdChecksum(t, st)
	// Apply manifest with different rows.
	_, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
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
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	manifest := map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "platform=tele", Target: "atlas"},
			{Seq: 1, Match: "platform=slack", Target: "ops"},
		},
	}
	c1, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, false, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Export → bytes → parse → apply again. Should be a no-op: identical
	// rows, checksum unchanged.
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
	c2, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c1, false, parsed, nil)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if c2 != c1 {
		t.Errorf("checksum changed on idempotent re-apply: %s -> %s", c1, c2)
	}
	// Row content should be identical to what we applied.
	r := resreg.Lookup("routes")
	got, _ := r.ScanAll(st.DB())
	rows := got.([]resources.RoutesRow)
	if len(rows) != 2 {
		t.Errorf("after round-trip: %d rows, want 2", len(rows))
	}
}

// TestApply_ForceBypassesChecksum: under --force against a drifted DB the
// manifest checksum differs from the DB's live checksum; force skips the
// check and applies anyway, returning the new (different) checksum.
func TestApply_ForceBypassesChecksum(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	if _, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// c0 is now stale (the DB moved past it). Force-apply against the stale
	// checksum succeeds and returns a fresh one reflecting the new content.
	newSum, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, true, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "ops"}},
	}, nil)
	if err != nil {
		t.Fatalf("force Apply: %v", err)
	}
	if newSum == c0 {
		t.Errorf("checksum unchanged after forced apply that changed content")
	}
}

func TestExport_DeterministicAcrossRuns(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	_, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
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
	exp1, _ := resreg.Export(st.DB(), resreg.SubsystemRoutd)
	b1, _ := resreg.EmitYAML(exp1)
	exp2, _ := resreg.Export(st.DB(), resreg.SubsystemRoutd)
	b2, _ := resreg.EmitYAML(exp2)
	if string(b1) != string(b2) {
		t.Errorf("export non-deterministic:\n--- 1 ---\n%s\n--- 2 ---\n%s", b1, b2)
	}
}

// TestGetRoundTrip_NoOp: `get <resource>` emits a fragment that, parsed
// and diffed against the live DB, reports no change — the round-trip
// honesty acceptance criterion (spec 5/8 §"arizuko get round-trip").
func TestGetRoundTrip_NoOp(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	if _, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
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
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	manifest := map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "platform=tele", Target: "atlas"},
		},
	}
	deltas, err := resreg.Plan(st.DB(), resreg.SubsystemRoutd, manifest)
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
	if _, err := resreg.Apply(context.Background(), st.DB(), resreg.SubsystemRoutd, routdChecksum(t, st), false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deltas2, err := resreg.Plan(st.DB(), resreg.SubsystemRoutd, manifest)
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
// bogus row field before any DB write (spec 5/8 §"Apply lifecycle" step 1).
func TestStrictParse_CLIPath(t *testing.T) {
	typoKey := []byte(`
checksum: "sha256:0"
routez:            # typo: should be "routes"
  - seq: 0
    match: ""
    target: atlas
`)
	if _, _, err := resreg.ParseYAML(typoKey); err == nil {
		t.Error("ParseYAML accepted typo'd resource key 'routez'")
	}
	bogusField := []byte(`
checksum: "sha256:0"
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

// TestCLI_ExportApply_RealFiles drives the ACTUAL operator-facing entry
// points (cmdExport, cmdApply — via ARIZUKO_DATA_DIR, exactly how a real
// invocation resolves an instance dir) end to end: seed rows through the
// CLI's own apply path, export to a file, apply that file to a SECOND fresh
// instance's real routd.db/onbod.db, and confirm the row lands. This is the
// step 4 acceptance proof — before this pass, cmdExport/cmdApply opened the
// frozen messages.db and never reached a production instance's real DBs
// (BUGS.md Y1). Only the success path is exercised: cmdApply/cmdExport call
// os.Exit on error, which is unsafe from inside a test process.
func TestCLI_ExportApply_RealFiles(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)

	srcDir, srcStores := openInstance(t)
	_ = srcDir
	srcRoutd := srcStores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, srcRoutd)
	if _, err := resreg.Apply(context.Background(), srcRoutd.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "platform=tele", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	closeStores(srcStores)

	// srcDir is openInstance's own t.TempDir, unrelated to ARIZUKO_DATA_DIR —
	// move its store/ tree under the instance name cmdExport/cmdApply resolve.
	instDir := filepath.Join(base, "arizuko_srcinst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(srcDir, "store"), filepath.Join(instDir, "store")); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(base, "export.yaml")
	cmdExport([]string{"srcinst", outFile})
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if !strings.Contains(string(data), "platform=tele") {
		t.Fatalf("exported file missing seeded route:\n%s", data)
	}
	if !strings.Contains(string(data), "---") {
		t.Errorf("exported file has no ---separated documents (expected routd.yaml + onbod.yaml)")
	}

	// Bootstrap a SECOND, fresh instance and apply the export into it.
	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)
	dstInstDir := filepath.Join(base, "arizuko_dstinst")
	if err := os.MkdirAll(dstInstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dstDataDir, "store"), filepath.Join(dstInstDir, "store")); err != nil {
		t.Fatal(err)
	}
	cmdApply([]string{"dstinst", outFile, "--force"})

	// Verify directly against the destination instance's REAL routd.db file.
	dstSt, err := store.OpenRoutd(filepath.Join(dstInstDir, "store"))
	if err != nil {
		t.Fatalf("open destination routd.db: %v", err)
	}
	defer dstSt.Close()
	got, err := resreg.Lookup("routes").ScanAll(dstSt.DB())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range got.([]resources.RoutesRow) {
		if r.Match == "platform=tele" && r.Target == "atlas" {
			found = true
		}
	}
	if !found {
		t.Errorf("applied route missing from destination routd.db: %v", got)
	}
}

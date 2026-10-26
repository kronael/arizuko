package main

// E2E test for `arizuko apply` + `arizuko export`. Exercises:
//   - parse: YAML decode for every resource kind we support
//   - CAS reject: stale checksum causes ErrChecksumMismatch
//   - CAS pass: matching checksum applies cleanly
//   - full rebuild: pre-existing rows wiped, manifest rows inserted
//   - round-trip: apply → export → apply produces a no-op (idempotent)
//
// openInstance below prepares BOTH owners the manifest spans in a namespace of
// this test's own. The bodies then drive resreg.Apply/Export/Plan/GetResource
// against the resulting *store.Store handles — the "orchestrator, not the thin
// CLI shim" philosophy — because the CLI wrappers (cmdApply/cmdExport/...) call
// os.Exit on error, which is unsafe to exercise from inside a test process.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// openInstance prepares the two owners the config manifest spans — routd's and
// onbod's — in a namespace of this test's own, and hands them back keyed the
// way openSubsystemStores hands them to cmdApply/cmdExport/cmdPlan/cmdGet.
func openInstance(t *testing.T) (string, map[string]*store.Store) {
	t.Helper()
	h := dyntest.Store(t)
	ctx := context.Background()
	for _, owner := range []string{store.OwnerRoutd, store.OwnerOnbod} {
		if err := store.PrepareOwner(ctx, h, owner); err != nil {
			t.Fatalf("prepare %s: %v", owner, err)
		}
	}
	for _, subsystem := range []string{resreg.SubsystemRoutd, resreg.SubsystemOnbod} {
		if err := resreg.EnsureSubsystem(ctx, h, h, subsystem); err != nil {
			t.Fatalf("ensure %s: %v", subsystem, err)
		}
	}
	if err := store.SeedRoutdFloor(ctx, h); err != nil {
		t.Fatalf("seed the routd floor: %v", err)
	}
	st := store.NewOver(h)
	dataDir := t.TempDir()

	t.Setenv("DATA_DIR", dataDir)
	seedCoreSource(t, filepath.Join(t.TempDir(), "release"))
	return dataDir, map[string]*store.Store{
		resreg.SubsystemRoutd: st,
		resreg.SubsystemOnbod: st,
	}
}

func routdChecksum(t *testing.T, st *store.Store) string {
	t.Helper()
	c, err := resreg.Checksum(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	return c
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
	_, err = resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, checksum, false, parsed, nil)
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
	newSum, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, checksum, false, parsed, nil)
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
	// A rule written outside the manifest path; Apply must wipe it. It goes in
	// through routes' own writer, which is where a rule an operator did not
	// declare actually comes from — add_route, a package install, a seed.
	r := resreg.Lookup("routes")
	if _, err := st.PutRouteRow(core.Route{
		Seq: 99, Match: "stale", Target: "atlas",
	}); err != nil {
		t.Fatal(err)
	}
	c0 := routdChecksum(t, st)
	_, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "", Target: "ops"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := r.ScanAll(resreg.On(st.Obj()))
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
	c1, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, false, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Export → bytes → parse → apply again. Should be a no-op: identical
	// rows, checksum unchanged.
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
	c2, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c1, false, parsed, nil)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if c2 != c1 {
		t.Errorf("checksum changed on idempotent re-apply: %s -> %s", c1, c2)
	}
	// Row content should be identical to what we applied.
	r := resreg.Lookup("routes")
	got, _ := r.ScanAll(resreg.On(st.Obj()))
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
	if _, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []resources.RoutesRow{{Seq: 0, Match: "", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// c0 is now stale (the DB moved past it). Force-apply against the stale
	// checksum succeeds and returns a fresh one reflecting the new content.
	newSum, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, true, map[string]any{
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
	_, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []resources.RoutesRow{
			{Seq: 0, Match: "z", Target: "atlas"},
			{Seq: 1, Match: "a", Target: "ops"},
		},
		"acl": []resources.ACLRow{
			{Principal: "user:bob", Action: "mcp:fetch_history", Scope: "atlas/", Effect: "allow"},
			{Principal: "user:alice", Action: "mcp:fetch_history", Scope: "atlas/", Effect: "allow"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exp1, _ := resreg.Export(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	b1, _ := resreg.EmitYAML(exp1)
	exp2, _ := resreg.Export(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	b2, _ := resreg.EmitYAML(exp2)
	if string(b1) != string(b2) {
		t.Errorf("export non-deterministic:\n--- 1 ---\n%s\n--- 2 ---\n%s", b1, b2)
	}
}

// TestGetRoundTrip_NoOp: `get <resource>` emits a fragment that, parsed
// and diffed against the live DB, reports no change — the round-trip
// honesty acceptance criterion (spec 5/8 §"Round-trip honesty").
func TestGetRoundTrip_NoOp(t *testing.T) {
	_, stores := openInstance(t)
	st := stores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, st)
	if _, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"acl": []resources.ACLRow{
			{Principal: "user:alice", Action: "mcp:fetch_history", Scope: "atlas/", Effect: "allow"},
			{Principal: "user:bob", Action: "tasks:*", Scope: "ops/", Effect: "allow"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	frag, err := resreg.GetResource(resreg.On(st.Obj()), "acl")
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
	d, err := resreg.Lookup("acl").Diff(resreg.On(st.Obj()), parsed["acl"])
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
	deltas, err := resreg.Plan(resreg.On(st.Obj()), resreg.SubsystemRoutd, manifest)
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
	if _, err := resreg.Apply(context.Background(), resreg.On(st.Obj()), resreg.SubsystemRoutd, routdChecksum(t, st), false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deltas2, err := resreg.Plan(resreg.On(st.Obj()), resreg.SubsystemRoutd, manifest)
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
// bogus row field before any DB write. The rule lives here and in
// ParseRows, not in a spec.
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

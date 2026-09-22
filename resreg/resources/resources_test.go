package resources

// Integration test for the resources package — Export + Apply against a real store
// DB. Each resource registers via init(); this verifies the engine can SELECT/INSERT
// every one against the real schema.
//
// The resources registered here span three owners (routd's acl/groups/routes,
// onbod's invites/onboarding, authd's keys), so the fixture prepares both of
// store's owners in one namespace. It is barred from routd.OpenMem: routd
// imports resreg/resources, so an internal test here cannot import routd back.
//
// It is NOT standing in for the split topology: Export/Apply/Plan below are
// always called with an EXPLICIT subsystem.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resregtest"
	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/objstore/dynstore"

	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

func openMem(t *testing.T) *dynstore.Store {
	t.Helper()
	db := dyntest.Store(t)
	ctx := context.Background()
	for _, owner := range []string{store.OwnerRoutd, store.OwnerOnbod} {
		if err := store.PrepareOwner(ctx, db, owner); err != nil {
			t.Fatalf("prepare %s: %v", owner, err)
		}
	}
	// A subsystem whose resources are entities applies as a versioned replace,
	// so the version MARKER has to exist before any write — a Bump is an Update
	// and an Update against a missing item refuses the whole batch it rides in
	// (objstore.EnsureScope). Each owner's own opener does this in migrateOwned.
	for _, subsystem := range []string{resreg.SubsystemOnbod, resreg.SubsystemRoutd} {
		if err := resreg.EnsureSubsystem(ctx, db, db, subsystem); err != nil {
			t.Fatalf("ensure %s: %v", subsystem, err)
		}
	}
	// The floor routd is BORN with. routd's own opener calls it; a seed is a row
	// an entity holds, so it lands after the entities are made (store/seed.go).
	if err := store.SeedRoutdFloor(ctx, db); err != nil {
		t.Fatalf("seed the routd floor: %v", err)
	}
	return db
}

// routdChecksum is a small helper so every Apply call in this file computes
// its CAS token fresh from the live DB, exactly as an operator's `arizuko
// apply` would (spec 5/8 §"Content-hash CAS": recomputed at read time, no
// stored counter).
func routdChecksum(t *testing.T, db *dynstore.Store) string {
	t.Helper()
	c, err := resreg.Checksum(resreg.On(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Checksum(resreg.OnDB(routd)): %v", err)
	}
	return c
}

func onbodChecksum(t *testing.T, db *dynstore.Store) string {
	t.Helper()
	c, err := resreg.Checksum(resreg.On(db), resreg.SubsystemOnbod)
	if err != nil {
		t.Fatalf("Checksum(resreg.OnDB(onbod)): %v", err)
	}
	return c
}

func TestExport_FreshDB(t *testing.T) {
	db := openMem(t)
	m, err := resreg.Export(resreg.On(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, ok := m["routes"]; !ok {
		t.Error("Export missing routes key")
	}
	nr, ok := m["network_rules"].([]NetworkRulesRow)
	if !ok {
		t.Fatalf("Export missing network_rules key")
	}
	// The floor: two instance-wide egress rules (store.SeedRoutdFloor).
	if len(nr) != 2 {
		t.Errorf("network_rules = %d rows, want 2 (the seeded floor)", len(nr))
	}
	acl, ok := m["acl"].([]ACLRow)
	if !ok {
		t.Fatalf("Export missing acl key")
	}
	// The floor: role:operator's root grant, role:member's twelve verbs, and the
	// twenty-two skip-approval rows that keep those verbs and the always-mounted
	// reads out of a folder's blanket hold.
	if len(acl) != 35 {
		t.Errorf("acl = %d rows, want 35 (the seeded floor)", len(acl))
	}
	root := false
	for _, r := range acl {
		if r.Principal == store.RoleOperator && r.Action == "*" && r.Scope == "**" {
			root = true
			if !r.GrantOption {
				t.Error("the delegation root exported without its grant option")
			}
		}
	}
	if !root {
		t.Error("Export dropped role:operator's root grant")
	}
}

func TestApply_RoundTrip_Routes(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	// added_by/added_via record WHO exercised routing authority over a target and
	// BY WHICH ACT. They took their defaults on every apply while the RowType
	// named five of eight columns, and 84 rows across three live instances lost
	// them (BUGS L265), so the round trip has to state that they survive.
	rows := []RoutesRow{
		{Seq: 0, Match: "platform=telegram room=123", Target: "atlas",
			AddedBy: "google:op", AddedVia: "add_route"},
		{Seq: 1, Match: "platform=slack", Target: "ops"},
	}
	c1, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if c1 == c0 {
		t.Errorf("checksum unchanged after apply that added routes")
	}
	r := resreg.Lookup("routes")
	got, err := r.ScanAll(resreg.On(db))
	if err != nil {
		t.Fatal(err)
	}
	gotRows := got.([]RoutesRow)
	if len(gotRows) != 2 {
		t.Errorf("after apply: %d rows, want 2", len(gotRows))
	}
	var attributed bool
	for _, g := range gotRows {
		if g.Target != "atlas" {
			continue
		}
		attributed = true
		if g.AddedBy != "google:op" || g.AddedVia != "add_route" {
			t.Errorf("attribution = (%q, %q), want it preserved through apply", g.AddedBy, g.AddedVia)
		}
		if g.ID == 0 {
			t.Error("the route carries no id after apply; delete_route(id) addresses that handle")
		}
	}
	if !attributed {
		t.Fatal("the atlas route is missing after apply")
	}
}

// TestApply_RoundTrip_ProxydRoutes_PreservesRedirectTo guards the data-loss
// regression where ProxydRoutesRow omitted the redirect_to column: an
// export→apply round-trip did DELETE+INSERT through the RowType and silently
// wiped redirect_to from every redirect route.
func TestApply_RoundTrip_ProxydRoutes_PreservesRedirectTo(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	rows := []ProxydRoutesRow{
		{Path: "/go", RedirectTo: "https://example.com/dest", Auth: "public"},
		{Path: "/api/", Backend: "http://svc:8080", Auth: "user"},
	}
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"proxyd_routes": rows,
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := resreg.Lookup("proxyd_routes").ScanAll(resreg.On(db))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, g := range got.([]ProxydRoutesRow) {
		if g.Path == "/go" {
			found = true
			if g.RedirectTo != "https://example.com/dest" {
				t.Errorf("redirect_to = %q, want it preserved through apply", g.RedirectTo)
			}
		}
	}
	if !found {
		t.Fatal("redirect route /go missing after apply")
	}
}

func TestApply_RoundTrip_NetworkRules(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	// Apply with empty network_rules wipes the seeded globals (apply is
	// full-rebuild). The operator must include them in the manifest.
	rows := []NetworkRulesRow{
		{Folder: "", Target: "anthropic.com"},
		{Folder: "", Target: "api.anthropic.com"},
		{Folder: "atlas", Target: "api.openai.com"},
	}
	_, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"network_rules": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := resreg.Lookup("network_rules")
	got, _ := r.ScanAll(resreg.On(db))
	gotRows := got.([]NetworkRulesRow)
	if len(gotRows) != 3 {
		t.Errorf("after apply: %d rows, want 3", len(gotRows))
	}
}

func TestApply_Groups_WithJSONBlob(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	rows := []GroupsRow{
		{
			Folder:             "atlas",
			ContainerConfig:    `{"Mounts":null,"Timeout":0,"MaxChildren":3}`,
			Product:            "assistant",
			Config:             `{"model":"claude-opus-4-7","open":1}`,
			CostCapCentsPerDay: 5000,
		},
	}
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": rows,
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := resreg.Lookup("groups")
	got, _ := r.ScanAll(resreg.On(db))
	gotRows := got.([]GroupsRow)
	if len(gotRows) != 1 {
		t.Fatalf("after apply: %d rows, want 1", len(gotRows))
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(gotRows[0].Config), &cfg); err != nil {
		t.Fatalf("config not JSON: %q (%v)", gotRows[0].Config, err)
	}
	if cfg.Model != "claude-opus-4-7" {
		t.Errorf("config.model = %q, want claude-opus-4-7", cfg.Model)
	}
	if gotRows[0].CostCapCentsPerDay != 5000 {
		t.Errorf("cost_cap = %d, want 5000", gotRows[0].CostCapCentsPerDay)
	}
}

func TestApply_Secrets_SkipsRebuild(t *testing.T) {
	db := openMem(t)
	// The imperative path: one live secret, written through its own writer.
	seedSecret(t, db, "atlas", "openai")
	c0 := routdChecksum(t, db)
	// Apply with EMPTY secrets list — must NOT wipe the row.
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"secrets": []SecretsRow{},
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := countSecrets(t, db, "atlas"); n != 1 {
		t.Errorf("secrets preserved? count=%d, want 1", n)
	}
}

// seedSecret writes one folder-scoped secret through store's own writer, which
// seals it: the value never travels, so what it is does not matter here.
func seedSecret(t *testing.T, db *dynstore.Store, folder, key string) {
	t.Helper()
	s := store.NewOver(db)
	s.SetSecretKeys([]byte("resources-test-key"))
	if err := s.SetSecret(store.ScopeFolder, folder, key, "ciphertext"); err != nil {
		t.Fatalf("seed secret %s/%s: %v", folder, key, err)
	}
}

func countSecrets(t *testing.T, db *dynstore.Store, folder string) int {
	t.Helper()
	keys, err := store.NewOver(db).SecretKeysIn(store.ScopeFolder, folder)
	if err != nil {
		t.Fatalf("SecretKeysIn(%q): %v", folder, err)
	}
	return len(keys)
}

func TestMembership_CycleRejected(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	// First apply: a -> b
	_, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	c1 := routdChecksum(t, db)
	// Now try b -> a: this would cycle. Apply is full-rebuild, so we have
	// to include the original edge AND the cycling one. Engine wipes,
	// inserts a→b OK, then tries b→a — cycle check sees a as parent of b.
	_, err = resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c1, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
			{Child: "b", Parent: "a"},
		},
	}, nil)
	if err == nil {
		t.Errorf("Apply with cycle: want error, got nil")
	}
	// A TRANSITIVE ring is the case a one-level check answers wrong, and the
	// manifest is where an operator would write one: three edges, none of them a
	// cycle on its own (BUGS L256).
	c2 := routdChecksum(t, db)
	_, err = resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c2, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
			{Child: "b", Parent: "c"},
			{Child: "c", Parent: "a"},
		},
	}, nil)
	if err == nil {
		t.Errorf("Apply with a three-edge ring: want error, got nil; every member of a " +
			"ring holds the union of the whole ring's authority")
	}
	// A SELF edge is a ring of length one and gets its own answer, because
	// "you are your own parent" is an operator mistake with a different fix.
	c3 := routdChecksum(t, db)
	_, err = resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c3, false, map[string]any{
		"acl_membership": []ACLMembershipRow{{Child: "a", Parent: "a"}},
	}, nil)
	if !errors.Is(err, ErrMembershipSelf) {
		t.Errorf("Apply with a self edge: err = %v, want %v", err, ErrMembershipSelf)
	}
}

// TestMembership_PairingRowsExcludedFromChecksumAndRebuild: added_by='pairing'
// edges (spec 5/31-identity-pairing's consented pairing links, added outside the manifest
// apply path) never enter acl_membership's content-hash checksum, its
// DELETE+INSERT rebuild, or a later Export — spec 5/8 CRITICAL finding
// ("Cross-subsystem apply"): a pre-image rollback built from Export must
// never be able to resurrect a pairing edge a caller revoked.
func TestMembership_PairingRowsExcludedFromChecksumAndRebuild(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"acl_membership": []ACLMembershipRow{{Child: "a", Parent: "b"}},
	}, nil); err != nil {
		t.Fatalf("seed role membership: %v", err)
	}
	c1 := routdChecksum(t, db)
	// Write a pairing edge the imperative way, not through resreg.Insert —
	// store's own membership writer, stamped added_by='pairing' exactly as the
	// redemption stamps it.
	if err := store.NewOver(db).PutMembership(
		"telegram:user/1", "google:alice", store.PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	c2 := routdChecksum(t, db)
	if c2 != c1 {
		t.Errorf("checksum changed after inserting a pairing edge: %s -> %s", c1, c2)
	}
	m, err := resreg.Export(resreg.On(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	for _, row := range m["acl_membership"].([]ACLMembershipRow) {
		if row.AddedBy == "pairing" {
			t.Errorf("Export emitted a pairing edge: %+v", row)
		}
	}
	// A subsequent apply that rebuilds acl_membership (mentions it in the
	// manifest) must leave the pairing edge alone.
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c2, false, map[string]any{
		"acl_membership": []ACLMembershipRow{{Child: "a", Parent: "b"}},
	}, nil); err != nil {
		t.Fatalf("rebuild apply: %v", err)
	}
	edges, err := store.NewOver(db).ParentEdges("telegram:user/1")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range edges {
		if e.Parent == "google:alice" && e.AddedBy == store.PairingAddedBy {
			n++
		}
	}
	if n != 1 {
		t.Errorf("pairing edge survived count = %d, want 1 (apply's rebuild must not touch it): %+v", n, edges)
	}
}

// TestApply_ScopedLeavesOutOfScopeRow: a partial manifest mentioning only
// folder "atlas" must NOT delete a live network_rules row for folder
// "ops" — scoped DELETE+INSERT (spec 5/8 §"Atomicity model"). Before the
// scoped-apply fix this was a wholesale DeleteAll that wiped "ops".
func TestApply_ScopedLeavesOutOfScopeRow(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	// Seed two folders' rules (network_rules has a clean Folder scope, no FK).
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.openai.com"},
			{Folder: "ops", Target: "api.pagerduty.com"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c1 := routdChecksum(t, db)
	// Partial manifest: only the atlas folder, with a different target.
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c1, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.anthropic.com"},
		},
	}, nil); err != nil {
		t.Fatalf("partial apply: %v", err)
	}
	r := resreg.Lookup("network_rules")
	got, _ := r.ScanAll(resreg.On(db))
	rows := got.([]NetworkRulesRow)
	var sawOps, sawAtlasNew, sawAtlasOld bool
	for _, row := range rows {
		switch {
		case row.Folder == "ops" && row.Target == "api.pagerduty.com":
			sawOps = true
		case row.Folder == "atlas" && row.Target == "api.anthropic.com":
			sawAtlasNew = true
		case row.Folder == "atlas" && row.Target == "api.openai.com":
			sawAtlasOld = true
		}
	}
	if !sawOps {
		t.Error("out-of-scope ops rule was deleted by a partial apply")
	}
	if !sawAtlasNew {
		t.Error("in-scope atlas rule was not rebuilt")
	}
	if sawAtlasOld {
		t.Error("in-scope atlas rule was not replaced (old target survived)")
	}
}

// TestDiff_IgnoresStampedTimestamp: a hand-written network_rules manifest
// that omits created_at reads as `unchanged` against a live row whose
// created_at was server-stamped — no phantom update (spec 5/8 step 3).
func TestDiff_IgnoresStampedTimestamp(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.openai.com"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Hand-written manifest row: no CreatedAt (operator never types a timestamp).
	hand := []NetworkRulesRow{{Folder: "atlas", Target: "api.openai.com"}}
	r := resreg.Lookup("network_rules")
	d, err := r.Diff(resreg.On(db), hand)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Changed() {
		t.Errorf("hand-written manifest phantom-updates on stamped created_at: %+v", d)
	}
	if len(d.Unchanged) != 1 {
		t.Errorf("Unchanged = %v, want one (atlas rule unchanged)", d.Unchanged)
	}
}

// TestPlanApplyAgree_Secrets: plan must not render a SkipApplyRebuild
// resource (secrets) as an actionable delta, because apply skips it.
// Plan and apply agree: the live secret row survives the apply, and the
// plan reports no change for secrets (spec 5/8 §"Secret safety").
func TestPlanApplyAgree_Secrets(t *testing.T) {
	db := openMem(t)
	seedSecret(t, db, "atlas", "openai")
	// Manifest declares a DIFFERENT secret triple than what's live.
	manifest := map[string]any{
		"secrets": []SecretsRow{{ScopeKind: "folder", ScopeID: "ops", Key: "github_token"}},
	}
	deltas, err := resreg.Plan(resreg.On(db), resreg.SubsystemRoutd, manifest)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, d := range deltas {
		if d.Resource == "secrets" && d.Changed() {
			t.Errorf("plan reports secrets as actionable change %+v; apply skips secrets", d)
		}
	}
	// Apply: the live secret row must survive (SkipApplyRebuild), so plan
	// (no change) and apply (no change to the row) agree.
	c0 := routdChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := countSecrets(t, db, "atlas"); n != 1 {
		t.Errorf("live secret wiped by apply (count=%d); plan said no change", n)
	}
}

// TestSecretsOpenAPIWriteOnly: the secrets resource declares explicit write-only
// Endpoints, so OpenAPI emits exactly POST /v1/secrets + DELETE /v1/secrets/{key}
// — NO read op (get/list) on either path. A sealed value must never surface in a
// read (spec 5/8 §"Secret safety"); this proves the doc can't drift into one.
func TestSecretsOpenAPIWriteOnly(t *testing.T) {
	out, err := resreg.OpenAPI("routd", "/", resregtest.Mounted("secrets"))
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	coll, ok := paths["/v1/secrets"].(map[string]any)
	if !ok {
		t.Fatalf("no /v1/secrets path in OpenAPI (paths=%v)", paths)
	}
	if _, has := coll["post"]; !has {
		t.Error("/v1/secrets missing POST create op")
	}
	if _, has := coll["get"]; has {
		t.Error("/v1/secrets exposes a GET read op — a sealed value must never be readable")
	}
	item, _ := paths["/v1/secrets/{key}"].(map[string]any)
	if item == nil {
		t.Fatalf("no /v1/secrets/{key} path in OpenAPI")
	}
	if _, has := item["delete"]; !has {
		t.Error("/v1/secrets/{key} missing DELETE op")
	}
	if _, has := item["get"]; has {
		t.Error("/v1/secrets/{key} exposes a GET read op")
	}
}

// TestApply_WritesOneAuditRow: an apply with ApplyOpts writes exactly one
// audit_log summary row (actor + manifest digest + final checksum), not one
// per resource (spec 5/8 §"CAS implementation" (3)).
func TestApply_WritesOneAuditRow(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	opts := &resreg.ApplyOpts{Actor: "op", ManifestDigest: "deadbeef"}
	newSum, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": []RoutesRow{
			{Seq: 0, Match: "platform=slack", Target: "ops"},
			{Seq: 1, Match: "platform=tele", Target: "atlas"},
		},
		"network_rules": []NetworkRulesRow{{Folder: "atlas", Target: "api.openai.com"}},
	}, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	applied := applyAuditRows(t, db)
	if len(applied) != 1 {
		t.Fatalf("recorded config.apply rows = %d, want exactly 1", len(applied))
	}
	actor, params := applied[0].Actor, applied[0].ParamsSummary
	if actor != "op" {
		t.Errorf("actor = %q, want op", actor)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		t.Fatalf("params_summary not JSON: %v (%s)", err, params)
	}
	if p["manifest_digest"] != "deadbeef" {
		t.Errorf("manifest_digest = %v, want deadbeef", p["manifest_digest"])
	}
	if p["checksum"] != newSum {
		t.Errorf("checksum = %v, want %q", p["checksum"], newSum)
	}
	res, _ := p["resources"].(map[string]any)
	if _, ok := res["routes"]; !ok {
		t.Errorf("resources summary missing routes: %v", res)
	}
	_ = audit.CategoryMutation // keep the audit import meaningful if asserts change
}

// The per-daemon ownership map that used to live here is DELETED, not moved. It was
// a hand-maintained COPY that could not fail — the expectation and the actual came
// from the same slice — and it had drifted again anyway.
//
// The replacement needs no ownership table: resreg/resregtest asserts each daemon's
// real mux against its real emitted doc, from the daemon's OWN package. A daemon
// advertising a foreign resource mounts none of its paths and fails there; one
// MOUNTING a foreign resource fails AssertServesNoForeignResource, whose foreign set
// is the complement of what the mux owns.

// TestOpenAPI_ProxydRoutesCarriesMCPDoc: the registered proxyd_routes
// catalog decl carries MCPDoc, so the emitted /openapi.json annotates the
// REST create operation with the agent-facing when-to-use string — the
// published doc IS the agent's REST catalog (no separate MCP tool needed).
func TestOpenAPI_ProxydRoutesCarriesMCPDoc(t *testing.T) {
	out, err := resreg.OpenAPI("proxyd", "/", resregtest.Mounted("proxyd_routes"))
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	post := doc["paths"].(map[string]any)["/v1/proxyd_routes"].(map[string]any)["post"].(map[string]any)
	want := "Create a proxyd route. Body fields mirror the TOML proxyd_route block."
	if post["description"] != want {
		t.Errorf("post description = %v, want %q", post["description"], want)
	}
	if post["x-mcp-when"] != want {
		t.Errorf("post x-mcp-when = %v, want %q", post["x-mcp-when"], want)
	}
}

// TestOpenAPI_EveryEmittedPathIsWellFormed walks the REGISTRY and, for every resource
// declaring a REST face, asserts the emitter renders at least one operation and
// renders every one under a real URL path.
//
// The second half is not hypothetical: the emitter keys `paths` on Endpoint.Path
// verbatim, so a resource whose Path is empty publishes under the "" key — a document
// that parses, advertises a real operationId, and from which no client can construct
// a request. An empty Path is invisible from the mounting side.
//
// A resource whose endpoints are all MCPOnly renders no path by design, and the
// exemption is derived from MCPOnly, not listed here.
func TestOpenAPI_EveryEmittedPathIsWellFormed(t *testing.T) {
	checked := 0
	for _, r := range resreg.All() {
		hasREST := slices.ContainsFunc(r.Endpoints, func(e resreg.Endpoint) bool { return !e.MCPOnly })
		if !hasREST {
			continue
		}
		checked++
		out, err := resreg.OpenAPI("probe", "/", resregtest.Mounted(r.Name))
		if err != nil {
			t.Fatalf("%s: OpenAPI: %v", r.Name, err)
		}
		var doc struct {
			Paths map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("%s: not JSON: %v", r.Name, err)
		}
		if len(doc.Paths) == 0 {
			t.Errorf("%s declares a non-MCPOnly endpoint but the emitter renders no path — "+
				"any daemon advertising it publishes an empty surface", r.Name)
		}
		for p := range doc.Paths {
			if !strings.HasPrefix(p, "/") {
				t.Errorf("%s emits an operation under path key %q — not a URL path, so no client "+
					"can construct the request and no mux can serve it", r.Name, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no registered resource declares a REST face — this guard would pass vacuously")
	}
}

// TestOpenAPI_RoutesTruthful: routd's /openapi.json for routes advertises the
// REAL mounted faces — PUT /v1/routes and DELETE /v1/routes/{id} — and NOT the
// PK-convention phantoms (PATCH/DELETE on /v1/routes/{seq}) it invented before
// the resource carried explicit Endpoints. This is the task #32 core assertion.
func TestOpenAPI_RoutesTruthful(t *testing.T) {
	out, err := resreg.OpenAPI("routd", "/", resregtest.Mounted("routes"))
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	paths := doc["paths"].(map[string]any)
	col, ok := paths["/v1/routes"].(map[string]any)
	if !ok {
		t.Fatalf("missing /v1/routes: %v", paths)
	}
	for _, m := range []string{"get", "post", "put"} {
		if _, ok := col[m]; !ok {
			t.Errorf("/v1/routes missing %s", m)
		}
	}
	item, ok := paths["/v1/routes/{id}"].(map[string]any)
	if !ok {
		t.Fatalf("missing real delete path /v1/routes/{id}: %v", paths)
	}
	if _, ok := item["delete"]; !ok {
		t.Errorf("/v1/routes/{id} missing delete")
	}
	// Phantom PK-convention paths must be gone.
	if _, ok := paths["/v1/routes/{seq}"]; ok {
		t.Errorf("phantom /v1/routes/{seq} still advertised")
	}
	if _, ok := item["patch"]; ok {
		t.Errorf("phantom PATCH on /v1/routes/{id}")
	}
}

// openAPIPaths emits the daemon's /openapi.json for the given resources and
// returns the set of path keys.
func openAPIPaths(t *testing.T, daemon string, owned []string) map[string]bool {
	t.Helper()
	out, err := resreg.OpenAPI(daemon, "/", resregtest.Mounted(owned...))
	if err != nil {
		t.Fatalf("%s OpenAPI: %v", daemon, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%s: not JSON: %v", daemon, err)
	}
	set := map[string]bool{}
	for p := range doc["paths"].(map[string]any) {
		set[p] = true
	}
	return set
}

// TestOpenAPI_OnboardingGatesTruthful: onbod serves the gate table at /v1/gates
// (hand-rolled), so the doc must too — never the resource-name path
// /v1/onboarding_gates the PK convention would guess.
func TestOpenAPI_OnboardingGatesTruthful(t *testing.T) {
	paths := openAPIPaths(t, "onbod", []string{"onboarding_gates"})
	if !paths["/v1/gates"] {
		t.Error("onboarding_gates: missing real GET /v1/gates")
	}
	if !paths["/v1/gates/{gate}"] {
		t.Error("onboarding_gates: missing real /v1/gates/{gate}")
	}
	if paths["/v1/onboarding_gates"] {
		t.Error("onboarding_gates: phantom /v1/onboarding_gates advertised")
	}
}

// TestFoldedEndpoints_RegistrySingleSource is DELETED, not moved. It compared a value
// to itself and could not see a MOUNT, which is the only place doc-vs-served drift
// ever lived.
//
// The claim it gestured at is now carried two ways that can fail: the document is
// DERIVED from the mux (BUGS F33), and each daemon pins its whole derived surface
// against a stated list via resregtest.AssertAdvertises using its REAL mux.

// TestFacadeMCP_RegistrySingleSource: each cold-tier facade resource the registry
// walk feeds ipc.ListTools (dashd's tool browser) carries the SAME exported
// MCPDoc/MCPArgs/MCPNames maps routd's *_resource.go feeds the agent socket, so the
// browser and the live agent read one owner and can't drift (spec 5/16, task #40).
func TestFacadeMCP_RegistrySingleSource(t *testing.T) {
	cases := []struct {
		name  string
		doc   map[resreg.Action]string
		args  map[resreg.Action][]resreg.MCPArg
		names map[resreg.Action]string
	}{
		{"routes", RoutesMCPDoc, RoutesMCPArgs, RoutesMCPNames},
		{"web_routes", WebRoutesMCPDoc, WebRoutesMCPArgs, WebRoutesMCPNames},
		{"scheduled_tasks", ScheduledTasksMCPDoc, ScheduledTasksMCPArgs, ScheduledTasksMCPNames},
		{"acl", ACLMCPDoc, ACLMCPArgs, ACLMCPNames},
		{"network_rules", NetworkRulesMCPDoc, NetworkRulesMCPArgs, NetworkRulesMCPNames},
		{"route_tokens", RouteTokensMCPDoc, RouteTokensMCPArgs, RouteTokensMCPNames},
		{"groups", GroupsMCPDoc, GroupsMCPArgs, GroupsMCPNames},
	}
	for _, c := range cases {
		got := resreg.Lookup(c.name)
		if got == nil {
			t.Errorf("%s not registered", c.name)
			continue
		}
		if len(got.MCPNames) == 0 {
			t.Errorf("%s: registered resource carries no MCPNames — the ListTools facade discriminator", c.name)
		}
		if !reflect.DeepEqual(got.MCPDoc, c.doc) {
			t.Errorf("%s: registered MCPDoc != exported var", c.name)
		}
		if !reflect.DeepEqual(got.MCPArgs, c.args) {
			t.Errorf("%s: registered MCPArgs != exported var", c.name)
		}
		if !reflect.DeepEqual(got.MCPNames, c.names) {
			t.Errorf("%s: registered MCPNames != exported var", c.name)
		}
	}
}

// seedInvite stores one live invite (keyed by ref = TokenRef(token), I1) and
// returns the WOULD-BE bearer — never itself persisted — so callers can assert
// it never appears anywhere it shouldn't. It writes through the owner's own
// importer rather than a fixture of its own, so the item shape this engine
// reads is the one store.ImportInvites writes.
func seedInvite(t *testing.T, db *dynstore.Store) string {
	t.Helper()
	const token = "live-bearer-do-not-export"
	now := time.Now().UTC()
	if _, err := store.NewOver(db).ImportInvites(context.Background(), []store.ArchiveInviteRow{{
		Ref: store.TokenRef(token), TargetGlob: "atlas/**", IssuedBySub: "agent:atlas",
		IssuedAt:  now.Format(time.RFC3339),
		ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		MaxUses:   3,
	}}); err != nil {
		t.Fatal(err)
	}
	return token
}

// liveInvites names every invite the database holds, so a test can say which
// survived an apply without reaching past the owner into the item shape.
func liveInvites(t *testing.T, db *dynstore.Store) map[string]bool {
	t.Helper()
	rows, err := store.NewOver(db).ExportInvites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Ref] = true
	}
	return out
}

// TestApply_NeverRebuildsInvites: invites are minted imperatively, so apply must
// never DELETE+INSERT the table. A manifest that MENTIONS invites (the case the
// engine would otherwise rebuild wholesale — an unmentioned resource is already
// skipped) must leave every live invite redeemable. Without SkipApplyRebuild this
// wipes the live bearer and replaces it with the manifest's, silently revoking
// access. Mirrors TestPlanApplyAgree_Secrets.
func TestApply_NeverRebuildsInvites(t *testing.T) {
	db := openMem(t)
	token := seedInvite(t, db)
	// Manifest declares a DIFFERENT invite than what's live.
	manifest := map[string]any{
		"invites": []InvitesRow{{
			Ref: "manifest-ref", TargetGlob: "ops/**",
			IssuedBySub: "agent:ops", IssuedAt: time.Now().UTC().Format(time.RFC3339),
			ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339), MaxUses: 1,
		}},
	}
	deltas, err := resreg.Plan(resreg.On(db), resreg.SubsystemOnbod, manifest)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, d := range deltas {
		if d.Resource == "invites" && d.Changed() {
			t.Errorf("plan reports invites as actionable change %+v; apply must skip invites", d)
		}
	}
	c0 := onbodChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), resreg.On(db), resreg.SubsystemOnbod, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	live := liveInvites(t, db)
	if !live[store.TokenRef(token)] {
		t.Errorf("live invite wiped by apply (%v) — apply revoked a live bearer", live)
	}
	if live["manifest-ref"] {
		t.Errorf("apply stored a manifest-declared invite (%v) — tokens are minted, never declared", live)
	}
}

// TestExport_OmitsInviteToken: `arizuko export` dumps every registered resource
// in a subsystem, so the manifest must not carry the invite bearer — holding
// the token IS the grant, and a dump lands in git/backups/paste (spec 5/8
// §"Secret safety", the same exclusion route_tokens/secrets get by omitting
// their hash/blob columns).
func TestExport_OmitsInviteToken(t *testing.T) {
	db := openMem(t)
	token := seedInvite(t, db)
	manifest, err := resreg.Export(resreg.On(db), resreg.SubsystemOnbod)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	out, err := resreg.EmitYAML(manifest)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	if bytes.Contains(out, []byte(token)) {
		t.Errorf("export emitted the live invite bearer %q:\n%s", token, out)
	}
	// The row itself still exports (target_glob is non-secret metadata) — only
	// the bearer is withheld, so this can't pass by dropping invites entirely.
	if !bytes.Contains(out, []byte("atlas/**")) {
		t.Errorf("export dropped the whole invites row; only the token must be withheld:\n%s", out)
	}
}

// TestRouteTokens_PairingExcludedFromExport: route_tokens' RowFilter
// (kind='route') keeps kind='pair' pairing links out of every
// manifest-visible read of this resource — spec 5/8 §"Secret and token
// values" (1): pairing tokens are 10-minute single-use credentials with no
// archival value, and must never appear in a YAML document at all.
func TestRouteTokens_PairingExcludedFromExport(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "atlas")
	s := store.NewOver(db)
	if err := s.InsertRouteToken(store.GenRouteToken(), store.RouteToken{
		JID: "web:atlas", OwnerFolder: "atlas", TargetFolder: "atlas",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssuePairingLink(context.Background(), "telegram:user/1", "atlas"); err != nil {
		t.Fatal(err)
	}
	got, err := resreg.Lookup("route_tokens").ScanAll(resreg.On(db))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]RouteTokensRow)
	if len(rows) != 1 || rows[0].JID != "web:atlas" {
		t.Fatalf("ScanAll = %v, want only the kind='route' row", rows)
	}
	manifest, err := resreg.Export(resreg.On(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	out, err := resreg.EmitYAML(manifest)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	if bytes.Contains(out, []byte("telegram:user/1")) {
		t.Errorf("export emitted a pairing token's jid:\n%s", out)
	}
}

// TestSecrets_NoAgentTool: the secrets resource carries no MCPDoc, and deriveMCPTools
// surfaces a tool only for actions that have one, so an agent gets no secret verb at
// all. That is the design, but it holds by OMISSION: adding an MCPDoc entry for a
// secret action would silently hand every agent a credential-write tool. The REST
// no-GET guards above stop a sealed value being READ; this stops it being written.
func TestSecrets_NoAgentTool(t *testing.T) {
	for _, r := range resreg.All() {
		if r.Name != SecretsName {
			continue
		}
		if len(r.MCPDoc) != 0 {
			t.Errorf("secrets declares MCPDoc %v — that derives an agent-facing tool; secrets are operator-only", r.MCPDoc)
		}
		if len(r.MCPNames) != 0 {
			t.Errorf("secrets declares MCPNames %v — the agent must have no secret verb", r.MCPNames)
		}
		return
	}
	t.Fatal("secrets resource not registered — this guard would pass vacuously")
}

// applyAuditRows reads back the rows one apply recorded, through the method
// every daemon answers GET /v1/audit with.
func applyAuditRows(t *testing.T, db *dynstore.Store) []audit.Row {
	t.Helper()
	rows, err := audit.Query(t.Context(), db, audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	var out []audit.Row
	for _, r := range rows {
		if r.Action == "config.apply" {
			out = append(out, r)
		}
	}
	return out
}

// TestRouteTokens_ManyTokensOnOneJIDStayDistinct: several live tokens sharing
// one jid each keep their own manifest identity.
//
// A jid is not unique and never was — a folder mints a chat link and any number
// of webhooks under the same one, and krons' `web:eval` carried 16 of the 43
// route_tokens rows. A manifest identity of `jid` files those 16 ingress
// credentials under one key, so Diff names one of them and `arizuko plan`
// counts 27 against a live 43 (BUGS L260). `token_hash` IS the item's
// coordinate, so PKFields names that and the count is the row count.
func TestRouteTokens_ManyTokensOnOneJIDStayDistinct(t *testing.T) {
	db := openMem(t)
	seedGroup(t, db, "eval")
	s := store.NewOver(db)
	// Three tokens on ONE jid, one on another: the krons shape in miniature.
	for _, jid := range []string{"web:eval", "web:eval", "web:eval", "hook:eval/github"} {
		if err := s.InsertRouteToken(store.GenRouteToken(), store.RouteToken{
			JID: jid, OwnerFolder: "eval", TargetFolder: "eval",
		}); err != nil {
			t.Fatalf("seed %s: %v", jid, err)
		}
	}
	r := resreg.Lookup("route_tokens")
	live, err := r.ScanAll(resreg.On(db))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := live.([]RouteTokensRow)
	if len(rows) != 4 {
		t.Fatalf("ScanAll = %d rows, want the 4 seeded", len(rows))
	}
	// Diff against an empty manifest: every live row is its own entry, so all
	// four are named. A manifest identity of `jid` makes this list two.
	d, err := r.Diff(resreg.On(db), nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Remove) != 4 {
		t.Errorf("Diff named %d of 4 live tokens (%v); tokens sharing a jid collapsed",
			len(d.Remove), d.Remove)
	}
	if n := len(slices.Compact(slices.Clone(d.Remove))); n != 4 {
		t.Errorf("Diff reported %d distinct identities for 4 tokens: %v", n, d.Remove)
	}
	if d.Live != 4 {
		t.Errorf("Live = %d, want 4 — the plan line counts rows, not identities", d.Live)
	}

	// EmitRows sorts by PK, so a non-unique PK leaves the order of the tied rows
	// to whatever order the scan handed them in — and that order is the export's
	// bytes, which are the subsystem checksum. A total order does not have that
	// degree of freedom: the same rows in any order emit the same document.
	first, err := r.EmitRows(rows)
	if err != nil {
		t.Fatalf("EmitRows: %v", err)
	}
	shuffled := slices.Clone(rows)
	slices.Reverse(shuffled)
	second, err := r.EmitRows(shuffled)
	if err != nil {
		t.Fatalf("EmitRows(shuffled): %v", err)
	}
	a, err := yaml.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := yaml.Marshal(second)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("EmitRows is order-dependent, so the checksum is too:\n%s\n---\n%s", a, b)
	}
}

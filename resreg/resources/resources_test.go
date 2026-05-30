package resources

// Integration test for the resources package — exercises Export +
// Apply against a real store DB (migrations 0001..0067 applied). Each
// resource registers via init(); this test verifies the engine can
// SELECT/INSERT every one against the real schema.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestExport_FreshDB(t *testing.T) {
	db := openMem(t)
	m, err := resreg.Export(db)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	v := m["config_version"].(int64)
	// Fresh DB: 2 seeded network_rules + 1 seeded acl (`role:operator,*,**`)
	// → bootstrap config_version = 3.
	if v != 3 {
		t.Errorf("config_version = %d, want 3 (2 network_rules + 1 acl seed)", v)
	}
	if _, ok := m["routes"]; !ok {
		t.Error("Export missing routes key")
	}
	if _, ok := m["network_rules"]; !ok {
		t.Error("Export missing network_rules key")
	}
}

func TestApply_RoundTrip_Routes(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	rows := []RoutesRow{
		{Seq: 0, Match: "platform=telegram room=123", Target: "atlas"},
		{Seq: 1, Match: "platform=slack", Target: "ops"},
	}
	v1, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"routes": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v1 != v0+1 {
		t.Errorf("version after apply = %d, want %d", v1, v0+1)
	}
	// Verify the rows landed.
	r := resreg.Lookup("routes")
	got, err := r.ScanAll(db)
	if err != nil {
		t.Fatal(err)
	}
	gotRows := got.([]RoutesRow)
	if len(gotRows) != 2 {
		t.Errorf("after apply: %d rows, want 2", len(gotRows))
	}
}

func TestApply_RoundTrip_NetworkRules(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	// Apply with empty network_rules wipes the seeded globals (apply is
	// full-rebuild). The operator must include them in the manifest.
	rows := []NetworkRulesRow{
		{Folder: "", Target: "anthropic.com"},
		{Folder: "", Target: "api.anthropic.com"},
		{Folder: "atlas", Target: "api.openai.com"},
	}
	_, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"network_rules": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := resreg.Lookup("network_rules")
	got, _ := r.ScanAll(db)
	gotRows := got.([]NetworkRulesRow)
	if len(gotRows) != 3 {
		t.Errorf("after apply: %d rows, want 3", len(gotRows))
	}
}

func TestApply_Groups_WithJSONBlob(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	rows := []GroupsRow{
		{
			Folder:             "atlas",
			ContainerConfig:    `{"Mounts":null,"Timeout":0,"MaxChildren":3}`,
			Product:            "assistant",
			Model:              "claude-opus-4-7",
			Open:               1,
			CostCapCentsPerDay: 5000,
		},
	}
	if _, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"groups": rows,
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := resreg.Lookup("groups")
	got, _ := r.ScanAll(db)
	gotRows := got.([]GroupsRow)
	if len(gotRows) != 1 {
		t.Fatalf("after apply: %d rows, want 1", len(gotRows))
	}
	if gotRows[0].Model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", gotRows[0].Model)
	}
	if gotRows[0].CostCapCentsPerDay != 5000 {
		t.Errorf("cost_cap = %d, want 5000", gotRows[0].CostCapCentsPerDay)
	}
}

func TestApply_Secrets_SkipsRebuild(t *testing.T) {
	db := openMem(t)
	// Manually insert a value row (the imperative path).
	_, err := db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"folder", "atlas", "openai", "v1:ciphertext",
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatal(err)
	}
	v0, _ := resreg.ConfigVersion(db)
	// Apply with EMPTY secrets list — must NOT wipe the row.
	_, err = resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"secrets": []SecretsRow{},
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&n)
	if n != 1 {
		t.Errorf("secrets preserved? count=%d, want 1", n)
	}
}

func TestMembership_CycleRejected(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	// First apply: a -> b
	_, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	v1, _ := resreg.ConfigVersion(db)
	// Now try b -> a: this would cycle. Apply is full-rebuild, so we have
	// to include the original edge AND the cycling one. Engine wipes,
	// inserts a→b OK, then tries b→a — cycle check sees a as parent of b.
	_, err = resreg.Apply(context.Background(), db, v1, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
			{Child: "b", Parent: "a"},
		},
	}, nil)
	if err == nil {
		t.Errorf("Apply with cycle: want error, got nil")
	}
}

// TestApply_ScopedLeavesOutOfScopeRow: a partial manifest mentioning only
// folder "atlas" must NOT delete a live network_rules row for folder
// "ops" — scoped DELETE+INSERT (spec 5/36 §"Atomicity model"). Before the
// scoped-apply fix this was a wholesale DeleteAll that wiped "ops".
func TestApply_ScopedLeavesOutOfScopeRow(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	// Seed two folders' rules (network_rules has a clean Folder scope, no FK).
	if _, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.openai.com"},
			{Folder: "ops", Target: "api.pagerduty.com"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	v1, _ := resreg.ConfigVersion(db)
	// Partial manifest: only the atlas folder, with a different target.
	if _, err := resreg.Apply(context.Background(), db, v1, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.anthropic.com"},
		},
	}, nil); err != nil {
		t.Fatalf("partial apply: %v", err)
	}
	r := resreg.Lookup("network_rules")
	got, _ := r.ScanAll(db)
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
// created_at was server-stamped — no phantom update (spec 5/36 step 3).
func TestDiff_IgnoresStampedTimestamp(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	if _, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.openai.com"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Hand-written manifest row: no CreatedAt (operator never types a timestamp).
	hand := []NetworkRulesRow{{Folder: "atlas", Target: "api.openai.com"}}
	r := resreg.Lookup("network_rules")
	d, err := r.Diff(db, hand)
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
// plan reports no change for secrets (spec 5/36 §"Secret safety").
func TestPlanApplyAgree_Secrets(t *testing.T) {
	db := openMem(t)
	if _, err := db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"folder", "atlas", "openai", "v1:ciphertext",
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	// Manifest declares a DIFFERENT secret triple than what's live.
	manifest := map[string]any{
		"secrets": []SecretsRow{{ScopeKind: "folder", ScopeID: "ops", Key: "github_token"}},
	}
	deltas, err := resreg.Plan(db, manifest)
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
	v0, _ := resreg.ConfigVersion(db)
	if _, err := resreg.Apply(context.Background(), db, v0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_id='atlas' AND key='openai'`).Scan(&n)
	if n != 1 {
		t.Errorf("live secret wiped by apply (count=%d); plan said no change", n)
	}
}

// TestApply_WritesOneAuditRow: an apply with ApplyOpts writes exactly one
// audit_log summary row (actor + manifest digest + final config_version),
// not one per resource (spec 5/36 §"CAS implementation" (3)).
func TestApply_WritesOneAuditRow(t *testing.T) {
	db := openMem(t)
	v0, _ := resreg.ConfigVersion(db)
	opts := &resreg.ApplyOpts{Actor: "op", ManifestDigest: "deadbeef"}
	newVer, err := resreg.Apply(context.Background(), db, v0, false, map[string]any{
		"routes": []RoutesRow{
			{Seq: 0, Match: "platform=slack", Target: "ops"},
			{Seq: 1, Match: "platform=tele", Target: "atlas"},
		},
		"network_rules": []NetworkRulesRow{{Folder: "atlas", Target: "api.openai.com"}},
	}, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='config.apply'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit_log config.apply rows = %d, want exactly 1", n)
	}
	var actor, params string
	if err := db.QueryRow(
		`SELECT actor, params_summary FROM audit_log WHERE action='config.apply'`,
	).Scan(&actor, &params); err != nil {
		t.Fatal(err)
	}
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
	if int64(p["config_version"].(float64)) != newVer {
		t.Errorf("config_version = %v, want %d", p["config_version"], newVer)
	}
	// Per-resource counts present for the two mentioned resources.
	res, _ := p["resources"].(map[string]any)
	if _, ok := res["routes"]; !ok {
		t.Errorf("resources summary missing routes: %v", res)
	}
	_ = audit.CategoryMutation // keep the audit import meaningful if asserts change
}

// daemonOwnership mirrors the per-daemon owned-resource lists the daemon
// mains pass to resreg.OpenAPIHandler (spec 5/36 §"OpenAPI emission").
// Keep in sync with timed/main.go, routd/cmd/routd/main.go,
// onbod/main.go, proxyd/main.go.
var daemonOwnership = map[string][]string{
	"timed":  {"scheduled_tasks"},
	"routd":  {"groups", "routes", "web_routes", "acl", "acl_membership", "secrets", "network_rules"},
	"onbod":  {"onboarding_gates"},
	"proxyd": {"proxyd_routes"},
	"runed":  {},
}

// TestOpenAPI_PerDaemonOwnership: each daemon's /openapi.json advertises
// ONLY its owned resources — never a foreign one. Before the fix, routd
// and runed passed nil (= all 10) and timed passed [] (= 0 owned paths).
func TestOpenAPI_PerDaemonOwnership(t *testing.T) {
	for daemon, owned := range daemonOwnership {
		out, err := resreg.OpenAPI(daemon, "/", owned)
		if err != nil {
			t.Fatalf("%s OpenAPI: %v", daemon, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("%s: not JSON: %v", daemon, err)
		}
		paths := doc["paths"].(map[string]any)
		ownedSet := map[string]bool{}
		for _, o := range owned {
			ownedSet[o] = true
		}
		for path := range paths {
			// path is "/v1/<name>" or "/v1/<name>/{pk}"; pull the <name>.
			name := strings.SplitN(strings.TrimPrefix(path, "/v1/"), "/", 2)[0]
			if !ownedSet[name] {
				t.Errorf("%s advertises foreign resource %q (path %q)", daemon, name, path)
			}
		}
		// Owned resources that exist in the registry must each appear.
		for _, o := range owned {
			if resreg.Lookup(o) == nil {
				continue
			}
			if _, ok := paths["/v1/"+o]; !ok {
				t.Errorf("%s missing owned resource path /v1/%s", daemon, o)
			}
		}
	}
}

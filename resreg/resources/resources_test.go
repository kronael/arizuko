package resources

// Integration test for the resources package — exercises Export +
// Apply against a real store DB (migrations 0001..0067 applied). Each
// resource registers via init(); this test verifies the engine can
// SELECT/INSERT every one against the real schema.
//
// store.Migrate's schema is the frozen pre-split messages.db shape, which
// happens to carry every table BOTH routd.db and onbod.db now own (it was
// forked from the same source at the split) — a convenient single-DB
// testbed for exercising the engine's SELECT/INSERT column shapes. It is
// NOT standing in for the split topology itself: Export/Apply/Plan below
// are always called with an EXPLICIT subsystem (resreg.SubsystemRoutd or
// resreg.SubsystemOnbod), matching what `cmd/arizuko` does against the
// REAL, separate routd.db/onbod.db files (see cmd/arizuko/apply_test.go for
// that end-to-end proof).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
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

// routdChecksum is a small helper so every Apply call in this file computes
// its CAS token fresh from the live DB, exactly as an operator's `arizuko
// apply` would (spec 5/8 §"Content-hash CAS": recomputed at read time, no
// stored counter).
func routdChecksum(t *testing.T, db *sql.DB) string {
	t.Helper()
	c, err := resreg.Checksum(db, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Checksum(routd): %v", err)
	}
	return c
}

func onbodChecksum(t *testing.T, db *sql.DB) string {
	t.Helper()
	c, err := resreg.Checksum(db, resreg.SubsystemOnbod)
	if err != nil {
		t.Fatalf("Checksum(onbod): %v", err)
	}
	return c
}

func TestExport_FreshDB(t *testing.T) {
	db := openMem(t)
	m, err := resreg.Export(db, resreg.SubsystemRoutd)
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
	// Fresh DB: 2 seeded global network_rules rows.
	if len(nr) != 2 {
		t.Errorf("network_rules = %d rows, want 2 (seeded globals)", len(nr))
	}
	acl, ok := m["acl"].([]ACLRow)
	if !ok {
		t.Fatalf("Export missing acl key")
	}
	// Fresh DB: 1 seeded acl row (`role:operator,*,**`).
	if len(acl) != 1 {
		t.Errorf("acl = %d rows, want 1 (seeded operator role)", len(acl))
	}
}

func TestApply_RoundTrip_Routes(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	rows := []RoutesRow{
		{Seq: 0, Match: "platform=telegram room=123", Target: "atlas"},
		{Seq: 1, Match: "platform=slack", Target: "ops"},
	}
	c1, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
		"routes": rows,
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if c1 == c0 {
		t.Errorf("checksum unchanged after apply that added routes")
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
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
		"proxyd_routes": rows,
	}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := resreg.Lookup("proxyd_routes").ScanAll(db)
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
	_, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
	c0 := routdChecksum(t, db)
	// Apply with EMPTY secrets list — must NOT wipe the row.
	_, err = resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
	c0 := routdChecksum(t, db)
	// First apply: a -> b
	_, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
	_, err = resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c1, false, map[string]any{
		"acl_membership": []ACLMembershipRow{
			{Child: "a", Parent: "b"},
			{Child: "b", Parent: "a"},
		},
	}, nil)
	if err == nil {
		t.Errorf("Apply with cycle: want error, got nil")
	}
}

// TestMembership_PairingRowsExcludedFromChecksumAndRebuild: added_by='pairing'
// edges (spec 5/31's consented pairing links, added outside the manifest
// apply path) never enter acl_membership's content-hash checksum, its
// DELETE+INSERT rebuild, or a later Export — spec 5/8 CRITICAL finding
// ("Cross-subsystem apply"): a pre-image rollback built from Export must
// never be able to resurrect a pairing edge a caller revoked.
func TestMembership_PairingRowsExcludedFromChecksumAndRebuild(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
		"acl_membership": []ACLMembershipRow{{Child: "a", Parent: "b"}},
	}, nil); err != nil {
		t.Fatalf("seed role membership: %v", err)
	}
	c1 := routdChecksum(t, db)
	// Insert a pairing edge directly (the imperative redemption path, not
	// through resreg.Insert — mirrors how routd's pairing redemption writes
	// it: raw SQL, added_by='pairing').
	if _, err := db.Exec(
		`INSERT INTO acl_membership (child, parent, added_by, added_at) VALUES (?, ?, 'pairing', ?)`,
		"telegram:user/1", "google:alice", time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	c2 := routdChecksum(t, db)
	if c2 != c1 {
		t.Errorf("checksum changed after inserting a pairing edge: %s -> %s", c1, c2)
	}
	m, err := resreg.Export(db, resreg.SubsystemRoutd)
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
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c2, false, map[string]any{
		"acl_membership": []ACLMembershipRow{{Child: "a", Parent: "b"}},
	}, nil); err != nil {
		t.Fatalf("rebuild apply: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child='telegram:user/1' AND parent='google:alice' AND added_by='pairing'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pairing edge survived count = %d, want 1 (apply's rebuild must not touch it)", n)
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
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
		"network_rules": []NetworkRulesRow{
			{Folder: "atlas", Target: "api.openai.com"},
			{Folder: "ops", Target: "api.pagerduty.com"},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c1 := routdChecksum(t, db)
	// Partial manifest: only the atlas folder, with a different target.
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c1, false, map[string]any{
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
// created_at was server-stamped — no phantom update (spec 5/8 step 3).
func TestDiff_IgnoresStampedTimestamp(t *testing.T) {
	db := openMem(t)
	c0 := routdChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
// plan reports no change for secrets (spec 5/8 §"Secret safety").
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
	deltas, err := resreg.Plan(db, resreg.SubsystemRoutd, manifest)
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
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_id='atlas' AND key='openai'`).Scan(&n)
	if n != 1 {
		t.Errorf("live secret wiped by apply (count=%d); plan said no change", n)
	}
}

// TestSecretsOpenAPIWriteOnly: the secrets resource declares explicit write-only
// Endpoints, so OpenAPI emits exactly POST /v1/secrets + DELETE /v1/secrets/{key}
// — NO read op (get/list) on either path. A sealed value must never surface in a
// read (spec 5/8 §"Secret safety"); this proves the doc can't drift into one.
func TestSecretsOpenAPIWriteOnly(t *testing.T) {
	out, err := resreg.OpenAPI("routd", "/", []string{"secrets"})
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
	newSum, err := resreg.Apply(context.Background(), db, resreg.SubsystemRoutd, c0, false, map[string]any{
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
	if p["checksum"] != newSum {
		t.Errorf("checksum = %v, want %q", p["checksum"], newSum)
	}
	// Per-resource counts present for the two mentioned resources.
	res, _ := p["resources"].(map[string]any)
	if _, ok := res["routes"]; !ok {
		t.Errorf("resources summary missing routes: %v", res)
	}
	_ = audit.CategoryMutation // keep the audit import meaningful if asserts change
}

// daemonOwnership mirrors the per-daemon owned-resource lists the daemon
// mains pass to resreg.OpenAPIHandler (spec 5/8 §"OpenAPI emission").
// Keep in sync with timed/split.go, routd/server.go OpenAPIResources,
// onbod/main.go, proxyd/main.go.
//
// It is a COPY, not the source: routd imports this package, so the real
// routd.OpenAPIResources cannot be read from here. It had drifted three
// resources in each direction — claiming acl_membership + network_rules,
// which routd advertises for neither, and missing route_tokens,
// installed_packages and scheduled_tasks (found while fixing BUGS F27). The
// authoritative doc↔mux check is routd's own
// TestOpenAPI_EveryAdvertisedPathIsMounted, which reads OpenAPIResources
// directly; what this table still buys is the cross-daemon half — no daemon
// advertises another's paths.
var daemonOwnership = map[string][]string{
	"timed": {"scheduled_tasks"},
	"routd": {
		"routes", "web_routes", "acl", "secrets", "route_tokens",
		"installed_packages", "scheduled_tasks", "groups",
	},
	"onbod":  {"onboarding_gates"},
	"proxyd": {"proxyd_routes"},
	"runed":  {},
}

// TestOpenAPI_ProxydRoutesCarriesMCPDoc: the registered proxyd_routes
// catalog decl carries MCPDoc, so the emitted /openapi.json annotates the
// REST create operation with the agent-facing when-to-use string — the
// published doc IS the agent's REST catalog (no separate MCP tool needed).
func TestOpenAPI_ProxydRoutesCarriesMCPDoc(t *testing.T) {
	out, err := resreg.OpenAPI("proxyd", "/", []string{"proxyd_routes"})
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

// TestOpenAPI_PerDaemonOwnership: each daemon's /openapi.json advertises
// ONLY its owned resources — never a foreign one. Before the fix, routd
// and runed passed nil (= all 10) and timed passed [] (= 0 owned paths).
func TestOpenAPI_PerDaemonOwnership(t *testing.T) {
	for daemon, owned := range daemonOwnership {
		daemonPaths := openAPIPaths(t, daemon, owned)
		// The doc must advertise exactly the union of its owned resources' own
		// path sets: no foreign path, and every owned resource contributes ≥1.
		// A path prefix need NOT equal the resource name — onboarding_gates is
		// served at /v1/gates — so compare against each resource's real emitted
		// paths, not a name-from-path heuristic.
		expected := map[string]bool{}
		for _, o := range owned {
			if resreg.Lookup(o) == nil {
				continue
			}
			own := openAPIPaths(t, daemon, []string{o})
			if len(own) == 0 {
				t.Errorf("%s: owned resource %q emits no path", daemon, o)
			}
			for p := range own {
				expected[p] = true
				if !daemonPaths[p] {
					t.Errorf("%s: missing owned path %q (resource %q)", daemon, p, o)
				}
			}
		}
		for p := range daemonPaths {
			if !expected[p] {
				t.Errorf("%s advertises foreign path %q", daemon, p)
			}
		}
	}
}

// openAPIPaths emits the daemon's /openapi.json for the given owned resources
// and returns the set of path keys.
func openAPIPaths(t *testing.T, daemon string, owned []string) map[string]bool {
	t.Helper()
	out, err := resreg.OpenAPI(daemon, "/", owned)
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

// TestOpenAPI_RoutesTruthful: routd's /openapi.json for routes advertises the
// REAL mounted faces — PUT /v1/routes and DELETE /v1/routes/{id} — and NOT the
// PK-convention phantoms (PATCH/DELETE on /v1/routes/{seq}) it invented before
// the resource carried explicit Endpoints. This is the task #32 core assertion.
func TestOpenAPI_RoutesTruthful(t *testing.T) {
	out, err := resreg.OpenAPI("routd", "/", []string{"routes"})
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

// TestFoldedEndpoints_RegistrySingleSource: the registered resource the doc
// walks carries the SAME exported Endpoints slice routd mounts, so the doc and
// the mount cannot drift.
func TestFoldedEndpoints_RegistrySingleSource(t *testing.T) {
	cases := []struct {
		name string
		want []resreg.Endpoint
	}{
		{"routes", RoutesEndpoints},
		{"web_routes", WebRoutesEndpoints},
		{"scheduled_tasks", ScheduledTasksEndpoints},
		{"acl", ACLEndpoints},
		{"network_rules", NetworkRulesEndpoints},
		{"onboarding_gates", OnboardingGatesEndpoints},
		{"route_tokens", RouteTokensEndpoints},
		{"groups", GroupsAgentEndpoints},
	}
	for _, c := range cases {
		got := resreg.Lookup(c.name)
		if got == nil {
			t.Errorf("%s not registered", c.name)
			continue
		}
		if !reflect.DeepEqual(got.Endpoints, c.want) {
			t.Errorf("%s: registered Endpoints != exported var (doc drift)", c.name)
		}
	}
}

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

// seedInvite inserts one live invite row (keyed by ref = TokenRef(token), I1)
// and returns the WOULD-BE bearer — never itself persisted — so callers can
// assert it never appears anywhere it shouldn't. expires_at is set non-NULL
// because InvitesRow scans it into a plain string with no COALESCE hook, so a
// NULL-expiry invite fails the engine scan outright.
func seedInvite(t *testing.T, db *sql.DB) string {
	t.Helper()
	const token = "live-bearer-do-not-export"
	now := time.Now().UTC()
	if _, err := db.Exec(
		`INSERT INTO invites (ref, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		store.TokenRef(token), "atlas/**", "agent:atlas", now.Format(time.RFC3339),
		now.Add(24*time.Hour).Format(time.RFC3339), 3, 0,
	); err != nil {
		t.Fatal(err)
	}
	return token
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
	deltas, err := resreg.Plan(db, resreg.SubsystemOnbod, manifest)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, d := range deltas {
		if d.Resource == "invites" && d.Changed() {
			t.Errorf("plan reports invites as actionable change %+v; apply must skip invites", d)
		}
	}
	c0 := onbodChecksum(t, db)
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemOnbod, c0, false, manifest, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM invites WHERE ref = ?`, store.TokenRef(token)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("live invite wiped by apply (count=%d) — apply revoked a live bearer", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM invites WHERE ref = 'manifest-ref'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("apply INSERTed a manifest-declared invite (count=%d) — tokens are minted, never declared", n)
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
	manifest, err := resreg.Export(db, resreg.SubsystemOnbod)
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
	if _, err := db.Exec(`INSERT INTO groups (folder, added_at, product) VALUES ('atlas', ?, 'assistant')`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO route_tokens (jid, owner_folder, token_hash, created_at, kind) VALUES (?, ?, ?, ?, ?)`,
		"web:atlas", "atlas", "hash-route", now, "route",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO route_tokens (jid, owner_folder, token_hash, created_at, kind) VALUES (?, ?, ?, ?, ?)`,
		"telegram:user/1", "atlas", "hash-pair", now, "pair",
	); err != nil {
		t.Fatal(err)
	}
	got, err := resreg.Lookup("route_tokens").ScanAll(db)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	rows := got.([]RouteTokensRow)
	if len(rows) != 1 || rows[0].JID != "web:atlas" {
		t.Fatalf("ScanAll = %v, want only the kind='route' row", rows)
	}
	manifest, err := resreg.Export(db, resreg.SubsystemRoutd)
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

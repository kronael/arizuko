package resources

// Integration test for the resources package — exercises Export +
// Apply against a real store DB (migrations 0001..0067 applied). Each
// resource registers via init(); this test verifies the engine can
// SELECT/INSERT every one against the real schema.

import (
	"context"
	"database/sql"
	"testing"
	"time"

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
	})
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
	})
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
	}); err != nil {
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
	})
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
	})
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
	})
	if err == nil {
		t.Errorf("Apply with cycle: want error, got nil")
	}
}

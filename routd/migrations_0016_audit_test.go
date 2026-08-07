package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestMigration0016AuditLogIgnoresLegacy proves routd.db gains audit_log, that
// audit emits land there, and that routd.Open does NOT read a sibling
// messages.db to fill it. audit's owner is routd.db; the one-time monolith copy
// belongs to `arizuko migrate-split` alone.
func TestMigration0016AuditLogIgnoresLegacy(t *testing.T) {
	dir := t.TempDir()

	// A pre-split messages.db carrying audit rows must be inert (store.Open runs
	// store/0066 → messages.db audit_log).
	msg, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open messages.db: %v", err)
	}
	if _, err := audit.EmitDB(context.Background(), msg.DB(), audit.Event{
		Category: audit.CategoryAuthZ,
		Action:   "acl.add",
		Actor:    "system",
		Folder:   "atlas/support",
		Outcome:  audit.OutcomeOK,
	}); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
	msg.Close()

	d, err := Create(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer d.Close()

	var n int
	if err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='acl.add'`).Scan(&n); err != nil {
		t.Fatalf("read audit_log from routd.db: %v", err)
	}
	if n != 0 {
		t.Errorf("routd.Open read the retired messages.db: got %d audit rows, want 0", n)
	}

	// A fresh emit against routd.db lands (routd's own mutation paths depend on
	// the table existing — previously it did not, forcing audit-free variants).
	if _, err := audit.EmitDB(context.Background(), d.SQL(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "route.add",
		Actor:    "system",
		Outcome:  audit.OutcomeOK,
	}); err != nil {
		t.Fatalf("emit into routd.db audit_log: %v", err)
	}
}

// TestRoutdMutationsAudit proves routd's OWN acl/secret mutations now emit an
// audit_log row into routd.db (finding: the wrappers used the audit-free store
// variants, so ACL/secret writes persisted with zero audit trail). One mutation
// → exactly one row.
func TestRoutdMutationsAudit(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer d.Close()
	// Route emissions into routd.db (EmitInTx writes the mutation's own tx; Init
	// only stamps the instance column — set it so the row carries it).
	audit.Init(d.SQL(), "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	if err := d.AddACLRow(core.ACLRow{
		Principal: "user:alice", Action: "admin", Scope: "atlas/eng", GrantedBy: "routd",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	if n := auditRows(t, d, "acl.add"); n != 1 {
		t.Errorf("acl.add audit rows = %d, want 1", n)
	}

	if err := d.SetSecret(store.ScopeFolder, "atlas/eng", "GITHUB_TOKEN", "ghp_x"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if n := auditRows(t, d, "secret.set"); n != 1 {
		t.Errorf("secret.set audit rows = %d, want 1", n)
	}
	// The row carries the instance stamp Init supplied.
	var inst string
	if err := d.SQL().QueryRow(
		`SELECT instance FROM audit_log WHERE action='secret.set'`).Scan(&inst); err != nil {
		t.Fatalf("read instance: %v", err)
	}
	if inst != "test" {
		t.Errorf("audit instance = %q, want test", inst)
	}
}

func auditRows(t *testing.T, d *DB, action string) int {
	t.Helper()
	var n int
	if err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action=?`, action).Scan(&n); err != nil {
		t.Fatalf("count audit_log %s: %v", action, err)
	}
	return n
}

// TestMigration0016AuditLogNoLegacyDB proves routd.Open is a clean no-op when
// there is no sibling messages.db (a fresh install) — the table exists, empty.
func TestMigration0016AuditLogNoLegacyDB(t *testing.T) {
	dir := t.TempDir()
	d, err := Create(dir)
	if err != nil {
		t.Fatalf("routd.Open on fresh dir: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.SQL().QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&n); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if n != 0 {
		t.Errorf("fresh audit_log = %d rows, want 0", n)
	}
}

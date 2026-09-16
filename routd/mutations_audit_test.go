package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestRoutdMutationsAudit proves routd's OWN acl/secret mutations emit an
// audit_log row (finding: the wrappers used the audit-free store variants, so
// ACL/secret writes persisted with zero audit trail). One mutation → exactly
// one row.
func TestRoutdMutationsAudit(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer d.Close()
	d.SetSecretKeys([]byte("audit-test-key")) // the store refuses unsealed writes (BUGS S2)
	// Init only stamps the instance column — the row rides the mutation's own
	// batch either way — so set it and the row carries the stamp.
	audit.Init(d.Obj(), "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	if err := addACLRow(d, core.ACLRow{
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
	if inst := auditRowsFor(t, d, "secret.set")[0].Instance; inst != "test" {
		t.Errorf("audit instance = %q, want test", inst)
	}
}

// auditRowsFor reads back every recorded row carrying one action, newest first,
// through the method /v1/audit answers with.
func auditRowsFor(t *testing.T, d *DB, action string) []audit.Row {
	t.Helper()
	rows, err := audit.Query(context.Background(), d.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	var out []audit.Row
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func auditRows(t *testing.T, d *DB, action string) int {
	t.Helper()
	return len(auditRowsFor(t, d, action))
}

// allAuditRows reads back everything recorded, newest first.
func allAuditRows(t *testing.T, d *DB) []audit.Row {
	t.Helper()
	rows, err := audit.Query(context.Background(), d.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	return rows
}

package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
)

// TestMigration0016AuditLog proves routd.db gains audit_log and that routd.Open
// backfills existing rows from a sibling messages.db (the pre-split shared sink
// dashd/proxyd/webd used to write). Guards the audit-owner move: audit now
// lands in routd.db, so messages.db can retire.
func TestMigration0016AuditLog(t *testing.T) {
	dir := t.TempDir()

	// Seed a pre-split messages.db with one audit_log row via the same insert
	// path the daemons use (store.Open runs store/0066 → messages.db audit_log).
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

	// routd.Open runs 0016 (schema) + copyLegacyAuditLog (backfill).
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	defer d.Close()

	// routd.db is where audit now writes + is read: the row must resolve here.
	var n int
	if err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='acl.add' AND folder='atlas/support'`,
	).Scan(&n); err != nil {
		t.Fatalf("read audit_log from routd.db: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log row not backfilled into routd.db: got %d, want 1", n)
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

	// Idempotent: re-opening must not duplicate the backfilled row.
	d.Close()
	d2, err := Open(dir)
	if err != nil {
		t.Fatalf("routd.Open (second): %v", err)
	}
	defer d2.Close()
	var m int
	if err := d2.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='acl.add'`).Scan(&m); err != nil {
		t.Fatalf("re-read audit_log: %v", err)
	}
	if m != 1 {
		t.Errorf("second open duplicated audit_log: got %d rows, want 1", m)
	}
}

// TestMigration0016AuditLogNoLegacyDB proves routd.Open is a clean no-op when
// there is no sibling messages.db (a fresh install) — the table exists, empty.
func TestMigration0016AuditLogNoLegacyDB(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
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

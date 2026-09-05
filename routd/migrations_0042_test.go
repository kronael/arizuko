package routd

import "testing"

// TestMigration0042DropsWriterlessSchema proves routd.db no longer carries
// the two shapes nothing writes: the idempotency_keys ledger (0042) and
// pane_sessions.last_status_at (0043). A schema claim with no writer reads
// as a feature in every schema page; the tables must say what the code does.
func TestMigration0042DropsWriterlessSchema(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var tables int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='idempotency_keys'`,
	).Scan(&tables); err != nil {
		t.Fatalf("probe sqlite_master: %v", err)
	}
	if tables != 0 {
		t.Error("idempotency_keys still present after migration 0042")
	}

	var cols int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('pane_sessions') WHERE name='last_status_at'`,
	).Scan(&cols); err != nil {
		t.Fatalf("probe pane_sessions columns: %v", err)
	}
	if cols != 0 {
		t.Error("pane_sessions.last_status_at still present after migration 0043")
	}
}

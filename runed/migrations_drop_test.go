package runed

import "testing"

// TestMigrationsDropRetiredTables — 0001 creates spawn_logs and mcp_tokens; both
// were retired by later drop migrations (0003, 0006) because neither ever got a
// writer. OpenMem runs the whole chain against a fresh DB, so this fails if a
// drop stops applying cleanly, and it fails if someone re-creates the table:
// an empty table is the only thing a schema reader sees, and it promises a
// spawn's output is queryable when it is not.
func TestMigrationsDropRetiredTables(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"spawn_logs", "mcp_tokens"} {
		var n int
		if err := db.db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil {
			t.Fatalf("sqlite_master(%s): %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s is still in the schema after its drop migration", table)
		}
	}

	// The tables that DO have writers must survive the drops.
	for _, table := range []string{"spawns", "session_log", "circuit_breaker"} {
		var n int
		if err := db.db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table).Scan(&n); err != nil {
			t.Fatalf("sqlite_master(%s): %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s must survive: a drop migration hit the wrong table", table)
		}
	}
}

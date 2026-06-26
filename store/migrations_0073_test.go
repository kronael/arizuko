package store

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration0073 seeds a pre-0073 groups table (model + open +
// observe_window_* columns) and verifies the migration folds them into the
// `config` JSON column, omitting NULLs, leaving container_config alone.
func TestMigration0073(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Pre-migration schema: groups as 0057+0060 leave it (the columns 0073
	// collapses), plus a FK referrer to prove DROP COLUMN keeps the FK intact.
	if _, err := db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE groups (
		  folder                  TEXT PRIMARY KEY,
		  added_at                TEXT NOT NULL,
		  container_config        TEXT,
		  product                 TEXT NOT NULL DEFAULT 'assistant',
		  open                    INTEGER NOT NULL DEFAULT 1,
		  observe_window_messages INTEGER,
		  observe_window_chars    INTEGER,
		  model                   TEXT
		);
		CREATE TABLE web_routes (
		  path_prefix TEXT PRIMARY KEY,
		  folder      TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE
		);
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	// full: every field set. bare: all defaults (open=1, NULLs).
	if _, err := db.Exec(
		`INSERT INTO groups
		   (folder, added_at, container_config, model, open,
		    observe_window_messages, observe_window_chars)
		 VALUES ('full','t0','{"MaxChildren":3}','claude-opus',0,25,8000),
		        ('bare','t1',NULL,NULL,1,NULL,NULL)`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO web_routes (path_prefix, folder) VALUES ('/p','full')`,
	); err != nil {
		t.Fatalf("seed web_route: %v", err)
	}

	sqlBytes, err := os.ReadFile("migrations/0073-groups-config-json.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply 0073: %v", err)
	}

	// Old columns must be gone.
	for _, col := range []string{"model", "open", "observe_window_messages",
		"observe_window_chars"} {
		if _, err := db.Exec(`SELECT ` + col + ` FROM groups`); err == nil {
			t.Errorf("column %q should be dropped", col)
		}
	}

	// full: every key folded into config.
	var model string
	var open, owm, owc sql.NullInt64
	db.QueryRow(
		`SELECT COALESCE(json_extract(config, '$.model'), ''),
		        json_extract(config, '$.open'),
		        json_extract(config, '$.observe_window_messages'),
		        json_extract(config, '$.observe_window_chars')
		 FROM groups WHERE folder='full'`,
	).Scan(&model, &open, &owm, &owc)
	if model != "claude-opus" {
		t.Errorf("full.model = %q, want claude-opus", model)
	}
	if !open.Valid || open.Int64 != 0 {
		t.Errorf("full.open = %v (valid=%v), want 0", open.Int64, open.Valid)
	}
	if !owm.Valid || owm.Int64 != 25 {
		t.Errorf("full.observe_window_messages = %v, want 25", owm.Int64)
	}
	if !owc.Valid || owc.Int64 != 8000 {
		t.Errorf("full.observe_window_chars = %v, want 8000", owc.Int64)
	}

	// container_config untouched.
	var cc string
	db.QueryRow(`SELECT container_config FROM groups WHERE folder='full'`).Scan(&cc)
	if cc != `{"MaxChildren":3}` {
		t.Errorf("full.container_config = %q, want unchanged", cc)
	}

	// bare: only open=1 folded; NULL fields omitted (json_extract → NULL).
	db.QueryRow(
		`SELECT json_extract(config, '$.open'),
		        json_extract(config, '$.model'),
		        json_extract(config, '$.observe_window_messages')
		 FROM groups WHERE folder='bare'`,
	).Scan(&open, &model, &owm)
	if !open.Valid || open.Int64 != 1 {
		t.Errorf("bare.open = %v (valid=%v), want 1", open.Int64, open.Valid)
	}
	owm = sql.NullInt64{}
	if db.QueryRow(
		`SELECT json_extract(config, '$.observe_window_messages')
		 FROM groups WHERE folder='bare'`).Scan(&owm); owm.Valid {
		t.Errorf("bare.observe_window_messages should be omitted, got %v", owm.Int64)
	}

	// DROP COLUMN preserved the FK: deleting the parent cascades.
	if _, err := db.Exec(`DELETE FROM groups WHERE folder='full'`); err != nil {
		t.Fatalf("cascade delete: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM web_routes`).Scan(&n)
	if n != 0 {
		t.Errorf("web_routes after cascade = %d, want 0 (FK lost)", n)
	}
}

package routd

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration0014 seeds a pre-0014 routd groups table (model +
// thread_replies + open + observe_window_* columns) and verifies the
// migration folds them into the `config` JSON column, omitting NULLs and
// preserving the web_routes FK via in-place DROP COLUMN.
func TestMigration0014(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE groups (
		  folder                  TEXT PRIMARY KEY,
		  added_at                TEXT NOT NULL,
		  container_config        TEXT,
		  product                 TEXT NOT NULL DEFAULT 'assistant',
		  cost_cap_cents_per_day  INTEGER NOT NULL DEFAULT 0,
		  open                    INTEGER NOT NULL DEFAULT 1,
		  observe_window_messages INTEGER,
		  observe_window_chars    INTEGER,
		  model                   TEXT,
		  thread_replies          INTEGER
		);
		CREATE TABLE web_routes (
		  path_prefix TEXT PRIMARY KEY,
		  folder      TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE
		);
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO groups
		   (folder, added_at, container_config, model, thread_replies, open,
		    observe_window_messages, observe_window_chars)
		 VALUES ('full','t0','{"MaxChildren":2}','claude-opus',1,0,25,8000),
		        ('bare','t1',NULL,NULL,NULL,1,NULL,NULL)`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO web_routes (path_prefix, folder) VALUES ('/p','full')`,
	); err != nil {
		t.Fatalf("seed web_route: %v", err)
	}

	sqlBytes, err := os.ReadFile("migrations/0014-groups-config-json.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply 0014: %v", err)
	}

	for _, col := range []string{"model", "thread_replies", "open",
		"observe_window_messages", "observe_window_chars"} {
		if _, err := db.Exec(`SELECT ` + col + ` FROM groups`); err == nil {
			t.Errorf("column %q should be dropped", col)
		}
	}

	var model string
	var tr, open, owm, owc sql.NullInt64
	db.QueryRow(
		`SELECT COALESCE(json_extract(config, '$.model'), ''),
		        json_extract(config, '$.thread_replies'),
		        json_extract(config, '$.open'),
		        json_extract(config, '$.observe_window_messages'),
		        json_extract(config, '$.observe_window_chars')
		 FROM groups WHERE folder='full'`,
	).Scan(&model, &tr, &open, &owm, &owc)
	if model != "claude-opus" {
		t.Errorf("full.model = %q, want claude-opus", model)
	}
	if !tr.Valid || tr.Int64 != 1 {
		t.Errorf("full.thread_replies = %v (valid=%v), want 1", tr.Int64, tr.Valid)
	}
	if !open.Valid || open.Int64 != 0 {
		t.Errorf("full.open = %v (valid=%v), want 0", open.Int64, open.Valid)
	}
	if !owm.Valid || owm.Int64 != 25 || !owc.Valid || owc.Int64 != 8000 {
		t.Errorf("full observe window = (%v,%v), want (25,8000)", owm.Int64, owc.Int64)
	}

	var cc string
	db.QueryRow(`SELECT container_config FROM groups WHERE folder='full'`).Scan(&cc)
	if cc != `{"MaxChildren":2}` {
		t.Errorf("full.container_config = %q, want unchanged", cc)
	}

	// bare: only open=1; thread_replies omitted.
	db.QueryRow(
		`SELECT json_extract(config, '$.open'),
		        json_extract(config, '$.thread_replies')
		 FROM groups WHERE folder='bare'`,
	).Scan(&open, &tr)
	if !open.Valid || open.Int64 != 1 {
		t.Errorf("bare.open = %v (valid=%v), want 1", open.Int64, open.Valid)
	}
	if tr.Valid {
		t.Errorf("bare.thread_replies should be omitted, got %v", tr.Int64)
	}

	if _, err := db.Exec(`DELETE FROM groups WHERE folder='full'`); err != nil {
		t.Fatalf("cascade delete: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM web_routes`).Scan(&n)
	if n != 0 {
		t.Errorf("web_routes after cascade = %d, want 0 (FK lost)", n)
	}
}

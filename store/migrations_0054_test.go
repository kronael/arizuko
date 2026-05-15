package store

import (
	"database/sql"
	"embed"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

//go:embed migrations/0054-route-target-fragment.sql
var migration0054FS embed.FS

// TestMigration0054 verifies the route impulse_config -> #observe
// conversion + duplicate trigger row spawning + column drop.
func TestMigration0054(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Pre-migration schema: routes table as 0053 leaves it, plus a
	// minimal messages table so the ALTER ADD COLUMN succeeds.
	if _, err := db.Exec(`
		CREATE TABLE routes (
		  id             INTEGER PRIMARY KEY AUTOINCREMENT,
		  seq            INTEGER NOT NULL DEFAULT 0,
		  match          TEXT    NOT NULL DEFAULT '',
		  target         TEXT    NOT NULL,
		  impulse_config TEXT
		);
		CREATE INDEX idx_routes_seq ON routes(seq);
		CREATE TABLE messages (
		  id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT,
		  content TEXT, timestamp TEXT, routed_to TEXT
		);
	`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	// Seed rows:
	//   id=1 sloth wildcard, all-zero weights        -> #observe, no spawn
	//   id=2 mixed: mention=100, message=0           -> #observe + spawn verb=mention
	//   id=3 bare, no impulse_config                 -> unchanged
	//   id=4 weights but none zero                   -> unchanged (no observe)
	seed := []struct {
		id     int
		seq    int
		match  string
		target string
		imp    string
	}{
		{1, 10, "room=sloth-room", "main", `{"weights":{"message":0,"mention":0}}`},
		{2, 5, "room=guild", "rhias/nemo", `{"weights":{"message":0,"mention":100}}`},
		{3, 0, "room=dm", "rhias/nemo", ""},
		{4, 1, "room=other", "rhias/nemo", `{"weights":{"mention":150}}`},
	}
	for _, r := range seed {
		var imp any
		if r.imp != "" {
			imp = r.imp
		}
		if _, err := db.Exec(
			`INSERT INTO routes (id, seq, match, target, impulse_config) VALUES (?, ?, ?, ?, ?)`,
			r.id, r.seq, r.match, r.target, imp,
		); err != nil {
			t.Fatalf("seed row %d: %v", r.id, err)
		}
	}

	// Apply migration 0054.
	sqlBytes, err := os.ReadFile("migrations/0054-route-target-fragment.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply 0054: %v", err)
	}

	// Verify column drop: SELECT impulse_config must fail.
	if _, err := db.Exec(`SELECT impulse_config FROM routes`); err == nil {
		t.Errorf("impulse_config column should be dropped")
	}

	// Verify is_observed column on messages.
	if _, err := db.Exec(`SELECT is_observed FROM messages`); err != nil {
		t.Errorf("is_observed column missing: %v", err)
	}

	// Read back routes.
	type r struct {
		seq            int
		match, target  string
	}
	rows, err := db.Query(`SELECT seq, match, target FROM routes ORDER BY seq, id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.seq, &x.match, &x.target); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}

	// Expectations:
	//   - bare row id=3 unchanged
	//   - all-zero row id=1 -> target=main#observe
	//   - mixed row id=2 -> rhias/nemo#observe + spawn (seq=4, match=room=guild verb=mention, target=rhias/nemo)
	//   - all-nonzero row id=4 unchanged
	want := []r{
		{4, "room=guild verb=mention", "rhias/nemo"},
		{5, "room=guild", "rhias/nemo#observe"},
		{0, "room=dm", "rhias/nemo"},
		{1, "room=other", "rhias/nemo"},
		{10, "room=sloth-room", "main#observe"},
	}
	// Sort by seq for comparison.
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d: %+v", len(got), len(want), got)
	}
	// Walk in seq order; build expected map.
	wantByMatch := make(map[string]r)
	for _, w := range want {
		wantByMatch[w.match] = w
	}
	for _, g := range got {
		w, ok := wantByMatch[g.match]
		if !ok {
			t.Errorf("unexpected row: %+v", g)
			continue
		}
		if g.seq != w.seq || g.target != w.target {
			t.Errorf("row %q = %+v, want %+v", g.match, g, w)
		}
	}
}

package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// usageRoutdDB builds an in-memory routd.db with the three tables the usage page
// reads. cost_log carries routd's column names (routd/migrations/0001:
// recorded_at / input_tokens / output_tokens / cost_cents) — NOT the pre-split
// store names (ts / input_tok / cents). The page used to query the store shape
// against the frozen messages.db, so seeding routd's shape here is what proves
// the repoint.
func usageRoutdDB(t *testing.T, folders ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE groups (folder TEXT PRIMARY KEY, name TEXT, added_at TEXT, parent TEXT)`,
		`CREATE TABLE cost_log (
			folder TEXT NOT NULL, turn_id TEXT NOT NULL, model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			cost_cents INTEGER NOT NULL DEFAULT 0, recorded_at TEXT NOT NULL,
			PRIMARY KEY (folder, turn_id, model))`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, chat_jid TEXT NOT NULL,
			is_bot_message INTEGER NOT NULL DEFAULT 0, timestamp TEXT NOT NULL,
			routed_to TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	for _, f := range folders {
		if _, err := db.Exec(`INSERT INTO groups (folder) VALUES (?)`, f); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func usageGet(t *testing.T, d *dash) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/usage/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestUsageNilRoutd: a nil routd handle renders the store-unavailable banner.
func TestUsageNilRoutd(t *testing.T) {
	code, body := usageGet(t, &dash{dbRoutd: nil})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "store unavailable") {
		t.Errorf("nil dbRoutd should banner store unavailable: %s", body)
	}
}

// TestUsageEmptyGroups: no groups → zero totals across the summary cards.
func TestUsageEmptyGroups(t *testing.T) {
	routd := usageRoutdDB(t)
	defer routd.Close()

	code, body := usageGet(t, &dash{dbRoutd: routd})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{"total messages", "tokens / 7d", "cost / 7d"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing card label %q: %s", want, body)
		}
	}
	if !strings.Contains(body, ">0<") {
		t.Errorf("empty groups should show a zero total: %s", body)
	}
	if !strings.Contains(body, "$0.00") {
		t.Errorf("empty groups should show $0.00 cost: %s", body)
	}
}

// TestUsageWithCost: a group with cost_log rows shows aggregated tokens/cents
// and a per-group breakdown row.
func TestUsageWithCost(t *testing.T) {
	routd := usageRoutdDB(t, "alice")
	defer routd.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := routd.Exec(
		`INSERT INTO cost_log (folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at)
		 VALUES ('alice', 't1', 'claude-opus-5', 800, 700, 250, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := routd.Exec(
		`INSERT INTO messages (id, chat_jid, is_bot_message, timestamp, routed_to)
		 VALUES ('m1','web:alice',0,?,'alice')`, now); err != nil {
		t.Fatal(err)
	}

	code, body := usageGet(t, &dash{dbRoutd: routd})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	// 1500 tokens → "1k"; 250 cents → "$2.50".
	if !strings.Contains(body, "1k") {
		t.Errorf("expected token total 1k: %s", body)
	}
	if !strings.Contains(body, "$2.50") {
		t.Errorf("expected cost total $2.50: %s", body)
	}
	if !strings.Contains(body, `href="/dash/groups/alice"`) {
		t.Errorf("per-group row should link to the group page: %s", body)
	}
}

// TestUsageNonOperatorForbidden: the usage page is operator-only.
func TestUsageNonOperatorForbidden(t *testing.T) {
	routd := usageRoutdDB(t)
	defer routd.Close()
	d := &dash{dbRoutd: routd}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/usage/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

package main

import (
	"context"
	"database/sql"
	"github.com/kronael/arizuko/core"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// usageRoutdDB is a migrated routd.db seeded with the given group folders. The
// page reads groups, cost_log and messages; cost_log's routd column names
// (recorded_at / input_tokens / output_tokens / cost_cents, NOT the pre-split
// store ts / input_tok / cents) now come from routd's own chain rather than a
// restatement of it.
func usageRoutdDB(t *testing.T, folders ...string) *sql.DB {
	t.Helper()
	db := routdDB(t)
	for _, f := range folders {
		if _, err := db.Exec(`INSERT INTO groups (folder, added_at) VALUES (?, '')`, f); err != nil {
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
	code, body := usageGet(t, &dash{dbRoutd: nil, routdURL: stubRoutd(t, nil)})
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

	code, body := usageGet(t, &dash{dbRoutd: routd, routdURL: stubRoutd(t, routd)})
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

	now := core.NowTS()
	if _, err := routd.Exec(
		`INSERT INTO cost_log (folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at)
		 VALUES ('alice', 't1', 'claude-opus-5', 800, 700, 250, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := routd.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, is_bot_message, timestamp, routed_to)
		 VALUES ('m1','web:alice','u1','hi',0,?,'alice')`, now); err != nil {
		t.Fatal(err)
	}

	code, body := usageGet(t, &dash{dbRoutd: routd, routdURL: stubRoutd(t, routd)})
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

// TestGroupUsageBulkBatchesLargeFolderLists: groupUsageBulk must not build a
// single IN(...) list sized to the whole folder count — that fails outright
// once SQLite's bound-parameter limit is exceeded. Seed more folders than
// usageBulkBatch and confirm every one still gets a row back.
func TestGroupUsageBulkBatchesLargeFolderLists(t *testing.T) {
	n := usageBulkBatch + 10
	folders := make([]string, n)
	for i := range folders {
		folders[i] = strings.Repeat("f", 1) + string(rune('a'+i%26)) + strconv.Itoa(i)
	}
	routd := usageRoutdDB(t, folders...)
	defer routd.Close()

	now := core.NowTS()
	if _, err := routd.Exec(
		`INSERT INTO cost_log (folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at)
		 VALUES (?, 't1', 'claude-opus-5', 10, 10, 5, ?)`, folders[n-1], now); err != nil {
		t.Fatal(err)
	}

	d := &dash{dbRoutd: routd, routdURL: stubRoutd(t, routd)}
	out, err := d.groupUsageBulk(context.Background(), folders)
	if err != nil {
		t.Fatalf("groupUsageBulk: %v", err)
	}
	if len(out) != n {
		t.Fatalf("got %d rows, want %d", len(out), n)
	}
	if out[n-1].Tokens7d != 20 {
		t.Errorf("last folder's cost_log row not aggregated: got %+v", out[n-1])
	}
}

// TestUsageNonOperatorForbidden: the usage page is operator-only.
func TestUsageNonOperatorForbidden(t *testing.T) {
	routd := usageRoutdDB(t)
	defer routd.Close()
	d := &dash{dbRoutd: routd, routdURL: stubRoutd(t, routd)}
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

// routd being unreachable must SAY so on the usage page. A zero spend column
// reads as "this instance costs nothing", which is false and is exactly the
// number an operator would relay upward.
func TestUsageSaysWhenRoutdIsUnreachable(t *testing.T) {
	routd := usageRoutdDB(t, "alice")
	defer routd.Close()

	// The folder list is read through routd's /v1 now, so an unreachable routd
	// fails at the list before any spend is summed. Either banner is a loud
	// failure; a 200 with NEITHER would be the bug — zero spend that is really
	// an unreachable owner.
	code, body := usageGet(t, &dash{dbRoutd: routd, routdURL: "http://127.0.0.1:1"})
	if code == 200 &&
		!strings.Contains(body, "usage query error") &&
		!strings.Contains(body, "folder query error") {
		t.Error("an unreachable routd rendered as zero spend")
	}
}

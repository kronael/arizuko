package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"

	"time"
)

// usageRoutdDB is a migrated routd.db seeded with the given group folders. The
// page reads groups and messages from here and the spend from routd's
// /v1/cost/summary, which the stub answers from stubMeter — the gateway holds
// that number and no SQLite does (spec 6/39).
func usageRoutdDB(t *testing.T, folders ...string) objstore.Store {
	t.Helper()
	db := routdDB(t)
	for _, f := range folders {
		seedFolder(t, db, f)
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
	code, body := usageGet(t, &dash{routd: nil, routdURL: stubRoutd(t, nil)})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "store unavailable") {
		t.Errorf("a nil routd handle should banner store unavailable: %s", body)
	}
}

// TestUsageEmptyGroups: no groups → zero totals across the summary cards.
func TestUsageEmptyGroups(t *testing.T) {
	routd := usageRoutdDB(t)

	code, body := usageGet(t, &dash{routd: routd, routdURL: stubRoutd(t, routd)})
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
	// A metered instance with nothing spent yet does report $0.00 — that IS a
	// measurement. Only an instance with no gateway shows "no meter" instead.
	if !strings.Contains(body, "$0.00") {
		t.Errorf("empty groups should show $0.00 cost: %s", body)
	}
}

// TestUsageWithCost: the page shows the tokens and cents the GATEWAY metered,
// aggregated and broken out per group.
func TestUsageWithCost(t *testing.T) {
	routd := usageRoutdDB(t, "alice")

	seedRoutdMessage(t, routd, core.Message{
		ID: "m1", ChatJID: "web:alice", Sender: "u1", Content: "hi",
		Timestamp: time.Now().UTC(), RoutedTo: "alice",
	})
	stubMeter(t, true, stubSpendRow{Folder: "alice", Tokens: 1500, CostCents: 250})

	code, body := usageGet(t, &dash{routd: routd, routdURL: stubRoutd(t, routd)})
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

// An instance with no model gateway has no meter, so the page says so instead
// of drawing $0.00 — a zero there reads as "nothing was spent", which is a
// measurement nobody took (spec 6/39).
func TestUsageSaysSoWhenThereIsNoMeter(t *testing.T) {
	routd := usageRoutdDB(t, "alice")
	stubMeter(t, false)

	code, body := usageGet(t, &dash{routd: routd, routdURL: stubRoutd(t, routd)})
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "no meter") {
		t.Errorf("unmetered instance must say so rather than show a figure: %s", body)
	}
	if strings.Contains(body, "$0.00") {
		t.Errorf("unmetered instance showed $0.00, which claims a measurement: %s", body)
	}
	if !strings.Contains(body, "No model gateway is set up") {
		t.Errorf("the page should name the missing gateway: %s", body)
	}
}

// TestGroupUsageBulkWholeFolderList: groupUsageBulk answers for EVERY folder it
// is asked about, however many that is. The page fans out over the whole group
// tree, and a read that silently truncated would show an instance's newest
// groups as never-used rather than erroring — so the assertions that matter are
// the row count and the metered row landing on the LAST folder of the list.
func TestGroupUsageBulkWholeFolderList(t *testing.T) {
	// Far past any real instance's group count (krons runs 95).
	const n = 510
	folders := make([]string, n)
	for i := range folders {
		folders[i] = strings.Repeat("f", 1) + string(rune('a'+i%26)) + strconv.Itoa(i)
	}
	routd := usageRoutdDB(t, folders...)
	stubMeter(t, true, stubSpendRow{Folder: folders[n-1], Tokens: 20, CostCents: 5})

	d := &dash{routd: routd, routdURL: stubRoutd(t, routd)}
	out, metered, err := d.groupUsageBulk(context.Background(), folders)
	if err != nil {
		t.Fatalf("groupUsageBulk: %v", err)
	}
	if !metered {
		t.Error("groupUsageBulk reported no meter where the summary said there is one")
	}
	if len(out) != n {
		t.Fatalf("got %d rows, want %d", len(out), n)
	}
	if out[n-1].Tokens7d != 20 {
		t.Errorf("last folder's metered spend not carried: got %+v", out[n-1])
	}
}

// TestUsageNonOperatorForbidden: the usage page is operator-only.
func TestUsageNonOperatorForbidden(t *testing.T) {
	routd := usageRoutdDB(t)
	d := &dash{routd: routd, routdURL: stubRoutd(t, routd)}
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

	// The folder list is read through routd's /v1 now, so an unreachable routd
	// fails at the list before any spend is summed. Either banner is a loud
	// failure; a 200 with NEITHER would be the bug — zero spend that is really
	// an unreachable owner.
	code, body := usageGet(t, &dash{routd: routd, routdURL: "http://127.0.0.1:1"})
	if code == 200 &&
		!strings.Contains(body, "usage query error") &&
		!strings.Contains(body, "folder query error") {
		t.Error("an unreachable routd rendered as zero spend")
	}
}

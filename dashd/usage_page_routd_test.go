package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
)

// TestUsagePageNumbersViaRealRoutd renders /dash/usage/ against routd's own
// Server over routd's own DB — no stub in the read path — and checks the
// numbers, not the status: the 7-day window excludes older spend, the totals
// sum the per-group cells, and a folder with nothing in the window shows the
// dash, never a stale figure.
func TestUsagePageNumbersViaRealRoutd(t *testing.T) {
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"alpha", "beta"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	recent := core.TS(now)
	stale := core.TS(now.Add(-8 * 24 * time.Hour))
	for _, r := range []struct {
		folder, turn, at string
		in, out, cents   int
	}{
		{"alpha", "t1", recent, 1200, 300, 250},
		{"alpha", "t2", recent, 100, 100, 5},
		{"beta", "t3", stale, 5000, 5000, 999},
	} {
		if _, err := db.SQL().Exec(
			`INSERT INTO cost_log (folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at)
			 VALUES (?, ?, 'claude-sonnet-4-5', ?, ?, ?, ?)`,
			r.folder, r.turn, r.in, r.out, r.cents, r.at); err != nil {
			t.Fatal(err)
		}
	}
	// Two human rows and one bot row on alpha: the per-group count is all rows
	// routed there, the volume table counts humans only.
	for _, m := range []struct {
		id  string
		bot int
	}{{"m1", 0}, {"m2", 0}, {"m3", 1}} {
		if _, err := db.SQL().Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, is_bot_message, timestamp, routed_to)
			 VALUES (?, 'web:alpha', 'u1', 'hi', ?, ?, 'alpha')`, m.id, m.bot, recent); err != nil {
			t.Fatal(err)
		}
	}

	// No verifier: routd answers dashd's calls as it would a service bearer
	// with an empty folder claim — every folder.
	ts := httptest.NewServer(routd.NewServer(db, nil, nil, nil, 0, "").Handler())
	t.Cleanup(ts.Close)

	code, body := usageGet(t, &dash{dbRoutd: db.SQL(), routdURL: ts.URL})
	if code != 200 {
		t.Fatalf("status = %d: %s", code, body)
	}
	for _, want := range []string{
		`<h3>3</h3>`,     // total messages: alpha's three rows
		`<h3>1k</h3>`,    // tokens / 7d: 1700 → "1k"
		`<h3>$2.55</h3>`, // cost / 7d: 255 cents
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary card %s missing:\n%s", want, body)
		}
	}
	alpha := strings.Index(body, `href="/dash/groups/alpha"`)
	beta := strings.Index(body, `href="/dash/groups/beta"`)
	if alpha < 0 || beta < 0 {
		t.Fatalf("per-group rows missing:\n%s", body)
	}
	if alpha > beta {
		t.Errorf("groups sort by 7d tokens descending; alpha must precede beta")
	}
	alphaRow := body[alpha:beta]
	for _, want := range []string{`<td>3</td>`, `<td>1k</td>`, `<td>$2.55</td>`} {
		if !strings.Contains(alphaRow, want) {
			t.Errorf("alpha row lacks %s: %s", want, alphaRow)
		}
	}
	betaRow := body[beta:]
	if end := strings.Index(betaRow, `</tr>`); end > 0 {
		betaRow = betaRow[:end]
	}
	if strings.Count(betaRow, `<td>—</td>`) != 4 {
		t.Errorf("beta has nothing in the window; want four dashes: %s", betaRow)
	}
	if strings.Contains(body, "$9.99") || strings.Contains(body, "10k") {
		t.Errorf("beta's 8-day-old spend leaked into the 7d window:\n%s", body)
	}
	today := now.Format("2006-01-02")
	if !strings.Contains(body, `<td>`+today+`</td><td>2</td>`) {
		t.Errorf("7-day volume must count today's two human rows:\n%s", body)
	}
}

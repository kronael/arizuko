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
// numbers, not the status.
//
// This routd names no model gateway, which is a supported deployment and the
// one with no meter. The page must then say so on every spend cell rather than
// draw a zero: nothing measured this instance, and "$0.00" would claim
// something did (spec 6/39). The message axis is arizuko's own data and is
// unaffected, so it still carries real counts — which is what makes this the
// end-to-end check that the two halves come from different owners.
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
	// The message axis is arizuko's own and still counts.
	if !strings.Contains(body, `<h3>3</h3>`) {
		t.Errorf("total messages card missing alpha's three rows:\n%s", body)
	}
	// The spend axis has no owner on this instance and must say so.
	if !strings.Contains(body, "no model gateway") {
		t.Errorf("an unmetered instance must name the missing gateway:\n%s", body)
	}
	if strings.Contains(body, "$0.00") {
		t.Errorf("unmetered instance drew $0.00, which claims a measurement:\n%s", body)
	}
	alpha := strings.Index(body, `href="/dash/groups/alpha"`)
	beta := strings.Index(body, `href="/dash/groups/beta"`)
	if alpha < 0 || beta < 0 {
		t.Fatalf("per-group rows missing:\n%s", body)
	}
	alphaRow := body[alpha:beta]
	if !strings.Contains(alphaRow, `<td>3</td>`) {
		t.Errorf("alpha row lost its message count: %s", alphaRow)
	}
	if strings.Count(alphaRow, `<td>no meter</td>`) != 2 {
		t.Errorf("alpha's token and cost cells must both read 'no meter': %s", alphaRow)
	}
	today := now.Format("2006-01-02")
	if !strings.Contains(body, `<td>`+today+`</td><td>2</td>`) {
		t.Errorf("7-day volume must count today's two human rows:\n%s", body)
	}
}

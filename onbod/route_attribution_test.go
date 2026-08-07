package main

// Spec 5/18 step 7: the route write is an act, not a side effect. Two halves,
// proved separately here.
//
//   - ATTRIBUTION. Every routes row onbod writes names who exercised routing
//     authority (added_by) and by which act (added_via). Before this, a row
//     appeared with neither and "why does this chat go here?" had no answer.
//   - BLAST RADIUS. create_world and invite redemption used to route EVERY JID
//     the sub had ever paired at the new target. A returning user who redeemed
//     an invite silently had their other chats re-routed — the seq-0 rows those
//     loops wrote outrank any higher-seq route the chat already had. Neither
//     writes routes at all now; /onboard's step-6 branch routes the ONE unrouted
//     JID, as an attributed act.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
)

// routeAttribution reads the (added_by, added_via) pair off the route matching
// `match`. Missing row → ("", "", false); a NULL column → "".
func routeAttribution(t *testing.T, db *sql.DB, match string) (string, string, bool) {
	t.Helper()
	var by, via sql.NullString
	err := db.QueryRow(
		`SELECT added_by, added_via FROM routes WHERE match = ?`, match).Scan(&by, &via)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("read route attribution for %q: %v", match, err)
	}
	return by.String, via.String, true
}

// countRoutes is the blast-radius probe: the whole point is how MANY rows an act
// wrote, not just whether the one it was about exists.
func countRoutes(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	return n
}

// TestPickerRouteCarriesAttribution — path 1, the step-6 picker. Replays the
// form the page actually renders (the F1 discipline) so the assertion covers the
// live submit path, not a synthesised one.
func TestPickerRouteCarriesAttribution(t *testing.T) {
	db := twoWorldUser(t)

	gw := getOnboard(db, "github:alice")
	form := formInputs(gw.Body.String())
	if form.Get("action") != "add_route" {
		t.Fatalf("no picker rendered, got fields %v", form)
	}

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:alice")
	for _, c := range gw.Result().Cookies() {
		post.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handleOnboardPost(pw, post, db, db, config{})
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", pw.Code, pw.Body.String())
	}

	by, via, ok := routeAttribution(t, db, "room=42")
	if !ok {
		t.Fatal("the chosen world wrote no route")
	}
	if by != "github:alice" {
		t.Errorf("added_by = %q, want the caller github:alice", by)
	}
	if via != routeViaPicker {
		t.Errorf("added_via = %q, want %q", via, routeViaPicker)
	}
}

// TestSoleWorldRouteCarriesAttribution — path 2. One administrable world offers
// no choice, so onbod writes the route itself; that it was NOT chosen is exactly
// what added_via has to record, or an operator cannot tell the two apart.
func TestSoleWorldRouteCarriesAttribution(t *testing.T) {
	db := twoWorldUser(t)
	db.Exec(`DELETE FROM groups WHERE folder = 'beta'`)

	if w := getOnboard(db, "github:alice"); w.Code != http.StatusOK {
		t.Fatalf("want 200 dashboard, got %d: %s", w.Code, w.Body.String())
	}

	by, via, ok := routeAttribution(t, db, "room=42")
	if !ok {
		t.Fatal("sole world wrote no route")
	}
	if by != "github:alice" {
		t.Errorf("added_by = %q, want the caller github:alice", by)
	}
	if via != routeViaSoleWorld {
		t.Errorf("added_via = %q, want %q", via, routeViaSoleWorld)
	}
	if via == routeViaPicker {
		t.Error("an unasked route claims the caller chose it")
	}
}

// TestInviteRedemptionRouteCarriesAttribution — path 3. Redemption itself writes
// no route; the attributed act is the /onboard landing it redirects to. Proved
// end to end (redeem, then follow the redirect) rather than by calling the
// dashboard directly, because "redemption produces an attributed route" is the
// claim, not "the dashboard does".
func TestInviteRedemptionRouteCarriesAttribution(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:bob', 'telegram:77', '2026-01-01')`)

	token := createInvite(t, db, "alice", "telegram:1", 1)
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, config{authBaseURL: "https://example.com"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem: want 303, got %d: %s", w.Code, w.Body.String())
	}
	if n := countRoutes(t, db); n != 0 {
		t.Fatalf("redemption wrote %d route(s) as a side effect; want 0", n)
	}

	if gw := getOnboard(db, "github:bob"); gw.Code != http.StatusOK {
		t.Fatalf("follow redirect: want 200, got %d: %s", gw.Code, gw.Body.String())
	}

	by, via, ok := routeAttribution(t, db, "room=77")
	if !ok {
		t.Fatal("the invited world never got the chat routed into it")
	}
	if by != "github:bob" {
		t.Errorf("added_by = %q, want the redeemer github:bob", by)
	}
	if via != routeViaSoleWorld {
		t.Errorf("added_via = %q, want %q", via, routeViaSoleWorld)
	}
}

// TestCreateWorldRouteCarriesAttribution — the fourth reachable flow, and the
// one that reaches a route write through create_world's redirect. create_world
// itself must write nothing.
func TestCreateWorldRouteCarriesAttribution(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:new', 'github:new', 'New', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:new', 'telegram:10', '2026-01-01')`)
	seedRedemption(t, db, "github:new", "/")

	if w := postOnboardSplit(db, db, "github:new", url.Values{
		"action": {"create_world"}, "username": {"newworld"}, "csrf": {"c"},
	}); w.Code != http.StatusSeeOther {
		t.Fatalf("create_world: want 303, got %d: %s", w.Code, w.Body.String())
	}
	if n := countRoutes(t, db); n != 0 {
		t.Fatalf("create_world wrote %d route(s) as a side effect; want 0", n)
	}

	if gw := getOnboard(db, "github:new"); gw.Code != http.StatusOK {
		t.Fatalf("follow redirect: want 200, got %d: %s", gw.Code, gw.Body.String())
	}

	by, via, ok := routeAttribution(t, db, "room=10")
	if !ok {
		t.Fatal("the new world never got the paired chat routed into it")
	}
	if by != "github:new" {
		t.Errorf("added_by = %q, want the creator github:new", by)
	}
	if via != routeViaSoleWorld {
		t.Errorf("added_via = %q, want %q", via, routeViaSoleWorld)
	}
}

// TestInviteRedemptionRoutesOnlyTheJIDItConcerns is the blast-radius regression.
// bob holds TWO paired chats: one already routed into a world he keeps, one not.
// The pre-step-7 loop wrote a seq-0 row for BOTH at the invite's target, and a
// seq-0 row outranks the seq-5 route the first chat already had (store/routes.go
// orders `seq ASC, id ASC`), so redeeming an invite silently moved a chat bob
// never mentioned into a world he had only just been invited to.
func TestInviteRedemptionRoutesOnlyTheJIDItConcerns(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alpha', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	// Already settled, at a seq the old seq-0 write would have outranked.
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (5, 'room=99', 'alpha')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:bob', 'telegram:99', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:bob', 'telegram:77', '2026-01-02')`)

	token := createInvite(t, db, "alice", "telegram:1", 1)
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, config{authBaseURL: "https://example.com"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem: want 303, got %d: %s", w.Code, w.Body.String())
	}

	// Redemption is a grant, full stop. One row in the table — the seeded one.
	if n := countRoutes(t, db); n != 1 {
		t.Fatalf("redemption changed the route table: %d rows, want the 1 seeded", n)
	}

	if gw := getOnboard(db, "github:bob"); gw.Code != http.StatusOK {
		t.Fatalf("follow redirect: want 200, got %d: %s", gw.Code, gw.Body.String())
	}

	// Exactly one new row, for the chat that had nowhere to go.
	if n := countRoutes(t, db); n != 2 {
		t.Errorf("landing wrote %d rows total, want 2 (the seeded one + room=77)", n)
	}
	var rows int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE match = 'room=99'`).Scan(&rows)
	if rows != 1 {
		t.Errorf("room=99 has %d routes; the settled chat gained a competing row", rows)
	}
	var target string
	db.QueryRow(`SELECT target FROM routes WHERE match = 'room=99'`).Scan(&target)
	if target != "alpha" {
		t.Errorf("room=99 now targets %q; a chat bob never named was moved", target)
	}
	if _, _, ok := routeAttribution(t, db, "room=77"); !ok {
		t.Error("the unrouted chat was left silent")
	}
}

// TestPreAttributionRoutesStayNull: existing rows are not backfilled with a
// sentinel. NULL means "no actor recorded" and must not read as a real one.
func TestPreAttributionRoutesStayNull(t *testing.T) {
	db := twoWorldUser(t)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'acme')`)

	by, via, ok := routeAttribution(t, db, "room=1")
	if !ok {
		t.Fatal("seeded route missing")
	}
	if by != "" || via != "" {
		t.Errorf("pre-attribution row claims added_by=%q added_via=%q; want NULL", by, via)
	}
}

// TestUnattributedRouteCannotBeWritten pins the single-writer property: onbod
// has exactly one routes INSERT, so no future path can reintroduce a row with
// no actor. Any row onbod produced in this suite's flows must name one.
func TestUnattributedRouteCannotBeWritten(t *testing.T) {
	db := twoWorldUser(t)
	db.Exec(`DELETE FROM groups WHERE folder = 'beta'`)
	getOnboard(db, "github:alice")

	rows, err := db.Query(`SELECT match FROM routes WHERE added_by IS NULL OR added_via IS NULL`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var match string
		rows.Scan(&match)
		t.Errorf("onbod wrote an unattributed route: match=%q", match)
	}
}

// The picker is authorization-bearing, not a convenience: a POST naming a world
// the caller does not administer is refused loudly, and the refusal must not
// leave an attributed row behind claiming otherwise.
func TestPickerRefusesAForeignWorldAndWritesNothing(t *testing.T) {
	db := twoWorldUser(t)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('someone-else', '2026-01-01')`)

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(url.Values{
		"action": {"add_route"}, "match": {"room=42"}, "target": {"someone-else"},
		auth.CSRFField: {"c"},
	}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:alice")
	post.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, post, db, db, config{})

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for a world alice does not administer, got %d: %s",
			w.Code, w.Body.String())
	}
	if n := countRoutes(t, db); n != 0 {
		t.Errorf("a refused route act still wrote %d row(s)", n)
	}
}

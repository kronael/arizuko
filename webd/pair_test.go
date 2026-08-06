package main

// Browser half of identity pairing (spec 5/31). The security properties, not
// just the happy path: GET must not consume, one parent per identity, and
// missing/expired/consumed tokens must be indistinguishable.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// seedPairingToken inserts a kind='pair' row created at `created`. Production
// mints through routd's issueRouteTokenTx; webd only ever reads and redeems.
func seedPairingToken(t *testing.T, st *store.Store, jid, folder string, created time.Time) string {
	t.Helper()
	raw := store.GenRouteToken()
	if _, err := st.DB().Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, kind) VALUES (?, ?, ?, ?, ?)`,
		store.TokenRefBytes(raw), jid, folder, created.Format(time.RFC3339Nano), store.RouteTokenKindPair,
	); err != nil {
		t.Fatalf("seed pairing token: %v", err)
	}
	return raw
}

// pairGET drives GET /pair/<token> as a signed-in user.
func pairGET(t *testing.T, s *server, token, sub string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/pair/"+token, nil)
	req.Header.Set("X-User-Sub", sub)
	req.Header.Set("X-User-Name", "Alice")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

// pairPOST drives POST /pair/<token> with a matching double-submit CSRF pair.
func pairPOST(t *testing.T, s *server, token, sub, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	body := url.Values{auth.CSRFField: {csrf}}.Encode()
	req := httptest.NewRequest("POST", "/pair/"+token, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	if csrf != "" {
		req.AddCookie(&http.Cookie{Name: pairCSRFCookie, Value: csrf})
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func parentsOf(t *testing.T, st *store.Store, child string) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT parent FROM acl_membership WHERE child = ? ORDER BY parent`, child)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// GET does not consume the token: two unfurl-style hits, then the POST still
// pairs. The CSRF token from the first GET is what the form echoes.
func TestPair_GetDoesNotConsume(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")
	token := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())

	var csrf string
	for i := range 2 {
		w := pairGET(t, s, token, "google:alice")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %d: status %d, body %s", i, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "telegram:user/1") {
			t.Errorf("GET %d: confirm page does not name the channel identity", i)
		}
		if !strings.Contains(w.Body.String(), "able to act as you") {
			t.Errorf("GET %d: confirm page does not state the consequence", i)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == pairCSRFCookie {
				csrf = c.Value
			}
		}
	}
	if csrf == "" {
		t.Fatal("GET did not set a CSRF cookie")
	}

	w := pairPOST(t, s, token, "google:alice", csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("POST after two GETs: status %d, body %s", w.Code, w.Body.String())
	}
	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 1 || got[0] != "google:alice" {
		t.Fatalf("parents = %v, want [google:alice]", got)
	}
}

// A POST without a matching CSRF pair writes nothing.
func TestPair_CSRFRequired(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")
	token := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())

	if w := pairPOST(t, s, token, "google:alice", ""); w.Code != http.StatusForbidden {
		t.Fatalf("no CSRF: status %d, want 403", w.Code)
	}
	// Cookie and field present but different.
	body := url.Values{auth.CSRFField: {"aaa"}}.Encode()
	req := httptest.NewRequest("POST", "/pair/"+token, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "google:alice")
	req.AddCookie(&http.Cookie{Name: pairCSRFCookie, Value: "bbb"})
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("mismatched CSRF: status %d, want 403", w.Code)
	}

	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 0 {
		t.Fatalf("a CSRF-less POST wrote %v", got)
	}
	if _, err := st.PeekPairing(token); err != nil {
		t.Errorf("a rejected POST consumed the token: %v", err)
	}
}

// A second, different parent is rejected with the ONE distinct error; the same
// parent again is an idempotent no-op.
func TestPair_OneParentPerIdentity(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")

	first := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())
	csrf := csrfFromGet(t, s, first, "google:alice")
	if w := pairPOST(t, s, first, "google:alice", csrf); w.Code != http.StatusOK {
		t.Fatalf("first pairing: %d %s", w.Code, w.Body.String())
	}

	second := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())
	w := pairPOST(t, s, second, "google:mallory", csrf)
	if w.Code != http.StatusConflict {
		t.Fatalf("second parent: status %d, want 409; body %s", w.Code, w.Body.String())
	}
	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 1 || got[0] != "google:alice" {
		t.Fatalf("parents = %v, want [google:alice]", got)
	}

	// Same parent again: allowed, still one edge.
	if w := pairPOST(t, s, second, "google:alice", csrf); w.Code != http.StatusOK {
		t.Fatalf("re-pair same parent: %d %s", w.Code, w.Body.String())
	}
	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 1 {
		t.Fatalf("re-pair duplicated the edge: %v", got)
	}
}

// Pairing writes the edge but does not route the JID, so a terminal success page
// leaves the chat the user just linked silent (spec 5/31 "What step 6 broke",
// BUGS P1b blocker 4). The success page must offer the way on to /onboard, whose
// step-6 branch does the routing — and ONLY the success page: an outcome where
// no edge was written has nothing to continue to, and pointing a refused or
// phished visitor at the onboarding dashboard would be its own bug.
//
// Falsified two ways: drop the link from the success branch and the first check
// fails; render it from pairPage (so every outcome carries it) and the conflict
// and unavailable checks fail.
func TestPair_SuccessPageContinuesToOnboard(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")
	const link = `href="` + pairContinueURL + `"`

	token := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())
	csrf := csrfFromGet(t, s, token, "google:alice")

	// The confirm page is a decision point, not an outcome: no way-on yet.
	if body := pairGET(t, s, token, "google:alice").Body.String(); strings.Contains(body, link) {
		t.Errorf("the confirm page offers the continue link before anything is linked: %s", body)
	}

	w := pairPOST(t, s, token, "google:alice", csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("pairing: %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, link) {
		t.Errorf("success page has no %s link, so the linked chat stays silent: %s", pairContinueURL, body)
	}

	// Conflict: someone else owns the identity, no edge written for this caller.
	second := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())
	w = pairPOST(t, s, second, "google:mallory", csrf)
	if w.Code != http.StatusConflict {
		t.Fatalf("second parent: status %d, want 409", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, link) {
		t.Errorf("the conflict page offers a continue link: %s", body)
	}

	// Unavailable token: nothing happened at all.
	w = pairGET(t, s, store.GenRouteToken(), "google:alice")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown token: status %d, want 404", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, link) {
		t.Errorf("the unavailable page offers a continue link: %s", body)
	}
}

// A role membership on the same JID does not block pairing (4e831f10).
func TestPair_RoleMembershipDoesNotBlock(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")
	if err := st.PutMembership("telegram:user/1", "role:member", "seed"); err != nil {
		t.Fatal(err)
	}
	token := seedPairingToken(t, st, "telegram:user/1", "hq", time.Now())
	csrf := csrfFromGet(t, s, token, "google:alice")

	if w := pairPOST(t, s, token, "google:alice", csrf); w.Code != http.StatusOK {
		t.Fatalf("blocked by a role membership: %d %s", w.Code, w.Body.String())
	}
	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 2 {
		t.Fatalf("parents = %v, want the pairing edge plus role:member", got)
	}
}

// Expired, consumed, unknown and malformed tokens share ONE response.
func TestPair_UnavailableIsIndistinguishable(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")

	expired := seedPairingToken(t, st, "telegram:user/1", "hq",
		time.Now().Add(-store.PairingTTL-time.Second))
	used := seedPairingToken(t, st, "telegram:user/2", "hq", time.Now())
	csrf := csrfFromGet(t, s, used, "google:alice")
	if w := pairPOST(t, s, used, "google:alice", csrf); w.Code != http.StatusOK {
		t.Fatalf("setup pairing failed: %d", w.Code)
	}

	// A delivery route token is not a pairing token either.
	routeTok := seedChatToken(t, st, "hq")

	var bodies []string
	for _, tok := range []string{expired, used, store.GenRouteToken(), routeTok, "not-a-token"} {
		w := pairGET(t, s, tok, "google:alice")
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %q: status %d, want 404", tok, w.Code)
		}
		bodies = append(bodies, w.Body.String())

		p := pairPOST(t, s, tok, "google:alice", csrf)
		if p.Code != http.StatusNotFound {
			t.Fatalf("POST %q: status %d, want 404", tok, p.Code)
		}
	}
	for i, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("response %d differs from the first — the four cases are distinguishable", i)
		}
	}
	if got := parentsOf(t, st, "telegram:user/1"); len(got) != 0 {
		t.Fatalf("an expired token wrote %v", got)
	}
}

// csrfFromGet runs the GET and returns the double-submit token it set.
func csrfFromGet(t *testing.T, s *server, token, sub string) string {
	t.Helper()
	w := pairGET(t, s, token, sub)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /pair: status %d, body %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == pairCSRFCookie {
			return c.Value
		}
	}
	t.Fatal("GET did not set the CSRF cookie")
	return ""
}

// The JWT sub arrives prefixed ("user:google:123", spec 5/1's sub prefix rule)
// but every stored principal is bare — acl.principal, acl_membership.parent.
// Storing the prefixed form makes the edge expand to a principal that matches
// no grant, so pairing succeeds and grants nothing. That is how it shipped
// (BUGS.md V1); this pins the strip so it cannot come back.
func TestPair_ParentIsStoredBare(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "hq", "hq")
	token := seedPairingToken(t, st, "telegram:user/900", "hq", time.Now())

	csrf := csrfFromGet(t, s, token, "user:google:123")
	if w := pairPOST(t, s, token, "user:google:123", csrf); w.Code != http.StatusOK {
		t.Fatalf("pair POST = %d, body %s", w.Code, w.Body.String())
	}

	got := parentsOf(t, st, "telegram:user/900")
	if len(got) != 1 || got[0] != "google:123" {
		t.Fatalf("stored parent = %v, want [google:123] — a prefixed parent "+
			"matches no acl row and the pairing grants nothing", got)
	}
}

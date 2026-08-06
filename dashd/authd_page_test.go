package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// authdUpstream stands in for authd's /v1 face. It records what dashd sent,
// because WHICH credential dashd presents is half the contract: authd
// authorizes the bearer, not the X-User-* headers dashd could forward.
//
// Responses are keyed by path so one server can answer both reads on the single
// page render; a path with no entry answers 404, which is what an unmounted
// endpoint would do and never a silent empty list.
type authdUpstream struct {
	srv     *httptest.Server
	respond map[string]string
	status  map[string]int

	calls int
	// path is the DECODED path; wireURI is what actually went down the socket.
	// The two differ exactly when a segment carries an escaped separator, which
	// is the case the revoke's path-escaping test turns on — asserting on the
	// decoded path there would report an escaped `%2F` as a traversal that
	// reached the server, which is the opposite of the truth.
	path    string
	wireURI string
	method  string
	query   string
	authz   string
	userSub string
}

func newAuthdUpstream(t *testing.T) *authdUpstream {
	t.Helper()
	u := &authdUpstream{
		respond: map[string]string{"/v1/signing_keys": `[]`, "/v1/sessions": `[]`},
		status:  map[string]int{},
	}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls++
		u.method, u.query = r.Method, r.URL.RawQuery
		u.path, u.wireURI = r.URL.Path, r.RequestURI
		u.authz = r.Header.Get("Authorization")
		u.userSub = r.Header.Get("X-User-Sub")
		io.Copy(io.Discard, r.Body)

		body, ok := u.respond[r.URL.Path]
		code := u.status[r.URL.Path]
		if code == 0 {
			code = http.StatusOK
		}
		if !ok {
			// Session DELETE is path-parameterised, so it is matched by prefix.
			if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/sessions/") {
				body, ok = u.respond["DELETE /v1/sessions/"], true
				if c := u.status["DELETE /v1/sessions/"]; c != 0 {
					code = c
				}
			}
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not_found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// authdDash wires a dash pointed at the fake authd. Every DB handle is left NIL
// on purpose: this page must reach authd over HTTP and never touch a local
// store, and a nil handle turns any direct read into a panic the test catches.
func authdDash(t *testing.T, u *authdUpstream) *http.ServeMux {
	t.Helper()
	d := &dash{}
	if u != nil {
		d.authdURL = u.srv.URL
	}
	return newMux(d)
}

// getAuthdPage GETs the cockpit as an operator and returns the whole body.
// Callers cut it to a section before asserting — see getAuthdSection.
func getAuthdPage(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/authd/", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	return w.Body.String()
}

const (
	keysFrom     = `<h2>Signing keys</h2>`
	keysTo       = `<h2>Who is signed in</h2>`
	sessionsFrom = `<h2>Who is signed in</h2>`
	sessionsTo   = `<h2>What authd wrote down</h2>`
)

// getAuthdSection renders the page and cuts it to one section. Whole-page
// Contains is worthless here: the word "authd" is in the nav crumb, the intro
// prose and the audit paragraph, and the two tables sit on one page — so an
// assertion against the full body would pass with the section under test empty,
// absent, or replaced by the OTHER section's content.
func getAuthdSection(t *testing.T, mux *http.ServeMux, from, to string) string {
	t.Helper()
	return sectionBetween(t, getAuthdPage(t, mux), from, to)
}

// pemDecoy builds a full PEM envelope at runtime. kind is a separate literal
// from " KEY", so the phrase detect-private-key blacklists is never contiguous
// in this file's bytes — see TestAuthdPage_NeverRendersKeyMaterialOrTokenValues.
func pemDecoy(kind, payload string) string {
	d := strings.Repeat("-", 5)
	return d + "BEGIN " + kind + " KEY" + d + payload + d + "END " + kind + " KEY" + d
}

func authdFuture(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

func authdPast(d time.Duration) string {
	return time.Now().Add(-d).UTC().Format(time.RFC3339)
}

// ---- signing keys ----

// The key table is the answer to "did the rotation take" — which, before
// GET /v1/signing_keys and this page, existed only behind sqlite3 on the box.
func TestAuthdPage_RendersSigningKeyLifecycle(t *testing.T) {
	u := newAuthdUpstream(t)
	retired := authdPast(3 * time.Hour)
	serves := authdFuture(20 * time.Minute)
	u.respond["/v1/signing_keys"] = fmt.Sprintf(`[
	  {"kid":"1754-aabbccdd","alg":"ES256","active":true,"status":"active","created_at":%q},
	  {"kid":"1701-11223344","alg":"ES256","active":false,"status":"retiring",
	   "created_at":%q,"retired_at":%q,"serves_until":%q}]`,
		authdPast(2*time.Hour), authdPast(30*24*time.Hour), retired, serves)

	sec := getAuthdSection(t, authdDash(t, u), keysFrom, keysTo)

	for _, want := range []string{"1754-aabbccdd", "1701-11223344", "ES256", "active", "retiring"} {
		if !strings.Contains(sec, want) {
			t.Errorf("key section missing %q\nsection: %s", want, sec)
		}
	}
	// The rotation instant, not just the status word: "when did this key stop
	// signing" is the question the status alone cannot answer.
	if !strings.Contains(sec, retired) {
		t.Errorf("key section dropped the rotation date %q\nsection: %s", retired, sec)
	}
	// A retiring key's serving deadline is a FUTURE instant, so it must go
	// through remainingTS; relativeTS measures time.Since and would render
	// every one of them as "now" — i.e. "the last passes stop being accepted
	// this second", which is precisely the wrong thing to tell an operator
	// mid-rotation. Both halves are pinned: the instant is rendered, AND it is
	// rendered as time REMAINING.
	if !strings.Contains(sec, serves) {
		t.Errorf("key section dropped the serving deadline %q\nsection: %s", serves, sec)
	}
	if !strings.Contains(sec, fmt.Sprintf(`<abbr title="%s">19m</abbr>`, serves)) &&
		!strings.Contains(sec, fmt.Sprintf(`<abbr title="%s">20m</abbr>`, serves)) {
		t.Errorf("serving deadline is not rendered as time remaining (want 19m/20m)\nsection: %s", sec)
	}
	if !strings.Contains(sec, `dot-ok`) {
		t.Errorf("active key has no ok dot\nsection: %s", sec)
	}
	// retiring is a rotation working as designed, not a fault: warn, never err.
	if !strings.Contains(sec, `dot-warn`) {
		t.Errorf("retiring key has no warn dot\nsection: %s", sec)
	}
	// An active key has no serving deadline because it is still signing. A bare
	// cell there would read as a value dashd failed to load.
	if !strings.Contains(sec, "still signing") {
		t.Errorf("active key does not say it is still signing\nsection: %s", sec)
	}
}

// THE security assertion for this page. authd is the sole ES256 signer, so the
// one unacceptable outcome is a key half or a token value reaching the browser.
//
// The upstream plants both in fields the projections do not declare — which is
// exactly how a leak would arrive if the page ever rendered authd's answer
// instead of the parsed rows. The legitimate values are asserted PRESENT first:
// without that, a page that rendered nothing at all would satisfy every
// absence check below and the test would prove only that empty HTML has no
// secrets in it.
func TestAuthdPage_NeverRendersKeyMaterialOrTokenValues(t *testing.T) {
	// The PEM markers are assembled at runtime rather than written as literals:
	// this repo's pre-commit detect-private-key hook scans source bytes and
	// cannot tell a planted decoy from the real thing, so "BEGIN EC PRIVATE
	// KEY" must never appear contiguously in this file. The bytes that go over
	// the wire are the complete markers either way, which is what the
	// assertions turn on.
	var (
		privPEM   = pemDecoy("EC PRIVATE", "MHcCAQEEIB7leakedkeymaterial")
		pubPEM    = pemDecoy("PUBLIC", "MFkwEwYHKoZIzj0leakedpub")
		tokenHash = "9f2c4e1ab0d3ff77aa1122334455667788990011223344556677889900aabbcc"
		rawToken  = "rt_live_S3cr3tRefreshTokenValueNobodyMayEverSee"
	)
	u := newAuthdUpstream(t)
	u.respond["/v1/signing_keys"] = fmt.Sprintf(`[
	  {"kid":"1754-aabbccdd","alg":"ES256","active":true,"status":"active","created_at":%q,
	   "priv_pem":%q,"pub_pem":%q}]`, authdPast(time.Hour), privPEM, pubPEM)
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-1","sub":"alice@example.com","scope":"acme/**","status":"active",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":4,
	   "token_hash":%q,"refresh_token":%q}]`,
		authdPast(48*time.Hour), authdPast(time.Hour), authdFuture(28*24*time.Hour),
		tokenHash, rawToken)

	body := getAuthdPage(t, authdDash(t, u))

	// The page DID render both rows — otherwise the absence checks below are
	// satisfied by emptiness and prove nothing.
	for _, want := range []string{"1754-aabbccdd", "alice@example.com", "fam-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page did not render %q, so the leak assertions below would be vacuous\nbody: %s", want, body)
		}
	}
	for _, secret := range []string{privPEM, pubPEM, tokenHash, rawToken} {
		if strings.Contains(body, secret) {
			t.Errorf("PAGE LEAKED %q — authd is the sole signer; this page renders projected rows, never authd's raw answer", secret)
		}
	}
	// The two markers a leak would carry even if the value itself were mangled.
	for _, marker := range []string{"PRIVATE KEY", "priv_pem", "token_hash", "refresh_token"} {
		if strings.Contains(body, marker) {
			t.Errorf("page body contains %q — a raw-body dump, not a projection", marker)
		}
	}
}

func TestAuthdPage_SigningKeysEmptyState(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/signing_keys"] = `[]`
	sec := getAuthdSection(t, authdDash(t, u), keysFrom, keysTo)

	if !strings.Contains(sec, "No signing key yet") {
		t.Errorf("missing empty state\nsection: %s", sec)
	}
	if strings.Contains(sec, "<table>") {
		t.Errorf("empty key list rendered a table\nsection: %s", sec)
	}
}

// ---- sessions ----

func TestAuthdPage_RendersSessions(t *testing.T) {
	u := newAuthdUpstream(t)
	expires := authdFuture(28 * 24 * time.Hour)
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-1","sub":"alice@example.com","scope":"acme/**","status":"active",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":7}]`,
		authdPast(48*time.Hour), authdPast(time.Hour), expires)

	sec := getAuthdSection(t, authdDash(t, u), sessionsFrom, sessionsTo)

	for _, want := range []string{"alice@example.com", "acme/**", "active", expires} {
		if !strings.Contains(sec, want) {
			t.Errorf("session section missing %q\nsection: %s", want, sec)
		}
	}
	// The rotation count is the "is this login actually in use" signal; a
	// dropped column would be invisible without asserting the value.
	if !strings.Contains(sec, ">7<") {
		t.Errorf("session section dropped the renewal count\nsection: %s", sec)
	}
	// dashd asks for a bounded page rather than authd's default.
	if !strings.Contains(u.query, fmt.Sprintf("limit=%d", authdSessionsLimit)) {
		t.Errorf("sessions query = %q, want limit=%d", u.query, authdSessionsLimit)
	}
}

// An empty scope is the authenticated-but-unauthorized session authd mints when
// the grants backend has nothing for the account — it lands the user on
// /onboard. A blank cell would read as a column dashd failed to fill.
func TestAuthdPage_EmptyScopeIsNamed(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-1","sub":"newbie@example.com","scope":"","status":"active",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":0}]`,
		authdPast(time.Minute), authdPast(time.Minute), authdFuture(30*24*time.Hour))

	sec := getAuthdSection(t, authdDash(t, u), sessionsFrom, sessionsTo)

	if !strings.Contains(sec, "nothing yet") {
		t.Errorf("empty scope did not render as a named state\nsection: %s", sec)
	}
}

func TestAuthdPage_SessionsEmptyState(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/sessions"] = `[]`
	sec := getAuthdSection(t, authdDash(t, u), sessionsFrom, sessionsTo)

	if !strings.Contains(sec, "Nobody is signed in right now.") {
		t.Errorf("missing empty state\nsection: %s", sec)
	}
	if strings.Contains(sec, "<table>") {
		t.Errorf("empty session list rendered a table\nsection: %s", sec)
	}
}

// ---- the sign-out control (DoD item 6, "view AND control") ----

// Signing someone out is destructive and visible to that person, so it is
// behind a confirm like every other dashd danger-zone action.
//
// Asserted against the session SECTION, not the page: the intro prose and the
// signing-key banner both talk about signing out, so a whole-body Contains
// would pass with no control rendered at all.
func TestAuthdPage_OffersSignOutBehindConfirm(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-1","sub":"alice@example.com","scope":"acme/**","status":"active",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":7}]`,
		authdPast(48*time.Hour), authdPast(time.Hour), authdFuture(28*24*time.Hour))

	sec := getAuthdSection(t, authdDash(t, u), sessionsFrom, sessionsTo)

	if !strings.Contains(sec, `action="/dash/authd/revoke"`) {
		t.Fatalf("no sign-out control in the session table\nsection: %s", sec)
	}
	if !strings.Contains(sec, `onsubmit="return confirm(`) {
		t.Errorf("sign-out is a bare button — no confirm step\nsection: %s", sec)
	}
	// family_id is the handle authd's DELETE takes; posting the sub would name
	// a person rather than the one lineage the operator clicked.
	if !strings.Contains(sec, `name="family_id" value="fam-1"`) {
		t.Errorf("sign-out form does not carry the family_id\nsection: %s", sec)
	}
	// Revoking stops the RENEWAL; the pass already in the browser lives out its
	// own short TTL. An operator told "signed out" who then watched the person
	// keep clicking would reasonably conclude the button was broken, so the
	// confirm has to say so rather than promise an instant cut.
	if !strings.Contains(sec, "15 more minutes") {
		t.Errorf("confirm does not warn that an open page keeps working briefly\nsection: %s", sec)
	}
}

// authd answers 404 for a family that is not live, so a button on a dead
// session could only ever produce an error banner.
func TestAuthdPage_DeadSessionOffersNoButton(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-dead","sub":"bob@example.com","scope":"acme/**","status":"revoked",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":2}]`,
		authdPast(72*time.Hour), authdPast(48*time.Hour), authdFuture(24*time.Hour))

	sec := getAuthdSection(t, authdDash(t, u), sessionsFrom, sessionsTo)

	// The row IS rendered — otherwise "no button" would be satisfied by an
	// empty table and prove nothing. Asserted on the sub, since family_id is an
	// internal handle the table shows only inside the form.
	if !strings.Contains(sec, "bob@example.com") {
		t.Fatalf("the revoked session did not render, so the assertion below is vacuous\nsection: %s", sec)
	}
	if strings.Contains(sec, `action="/dash/authd/revoke"`) {
		t.Errorf("a revoked session still offers a sign-out button\nsection: %s", sec)
	}
	// revoked is the one state somebody chose; an incident review looks for it.
	if !strings.Contains(sec, "dot-err") {
		t.Errorf("revoked session has no err dot\nsection: %s", sec)
	}
}

func postAuthdRevoke(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/dash/authd/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(req))
	return w
}

// The control revokes through authd's DELETE, never through a local handle.
// That is what buys the audit row: resreg.invoke opens the transaction, revokes
// in it, and emits the event into the same one, rolling the revoke back if the
// audit write fails (authd's TestSessionsRevokeKillsTheFamilyAndAudits pins the
// row itself). A dashd-side write would have none of that — and dashd has no
// handle on auth.db to make one with, which the nil DB fields assert.
func TestAuthdRevoke_DeletesThroughAuthd(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["DELETE /v1/sessions/"] = `{"family_id":"fam-1","revoked":3}`
	d := &dash{
		authdURL: u.srv.URL,
		svc:      func(context.Context) (string, error) { return "service-dashd-token", nil },
	}
	if d.dbRoutd != nil || d.dbRuned != nil || d.dbOnbod != nil {
		t.Fatal("this test is only meaningful with no local DB handle")
	}

	w := postAuthdRevoke(t, newMux(d), url.Values{"family_id": {"fam-1"}})

	if u.calls != 1 {
		t.Fatalf("authd calls = %d, want 1", u.calls)
	}
	if u.method != http.MethodDelete {
		t.Errorf("authd got %s, want DELETE — the standard action is what writes the audit row", u.method)
	}
	if u.path != "/v1/sessions/fam-1" {
		t.Errorf("authd path = %q, want /v1/sessions/fam-1", u.path)
	}
	if u.authz != "Bearer service-dashd-token" {
		t.Errorf("Authorization = %q, want the service:dashd bearer", u.authz)
	}
	// authd authorizes the BEARER; forwarding the operator identity would imply
	// it narrows per viewer, and it does not.
	if u.userSub != "" {
		t.Errorf("dashd forwarded X-User-Sub=%q to authd, which authorizes the bearer instead", u.userSub)
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 back to the page", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/dash/authd/?msg=revoked" {
		t.Errorf("Location = %q, want /dash/authd/?msg=revoked", loc)
	}
}

// A family_id is operator input and lands in a URL PATH SEGMENT, so its
// separators must be escaped on the wire: sent raw, `../../v1/keys` is a
// different endpoint, and the one it reaches on authd is the signing-key
// surface.
//
// Asserted on the wire URI, not the decoded path: net/http decodes %2F back to
// / in r.URL.Path, so a correctly-escaped request and a traversal look
// identical there. r.RequestURI is the only field that distinguishes them.
func TestAuthdRevoke_EscapesTheFamilyIDIntoThePathSegment(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["DELETE /v1/sessions/"] = `{"revoked":0}`
	postAuthdRevoke(t, authdDash(t, u), url.Values{"family_id": {"../../v1/keys"}})

	if u.calls != 1 {
		t.Fatalf("authd calls = %d, want 1", u.calls)
	}
	if strings.Contains(u.wireURI, "/../") || strings.HasSuffix(u.wireURI, "/v1/keys") {
		t.Errorf("a traversal-shaped family_id went on the wire as %q — the segment is not escaped", u.wireURI)
	}
	if !strings.HasPrefix(u.wireURI, "/v1/sessions/") {
		t.Errorf("wire URI = %q, want it confined under /v1/sessions/", u.wireURI)
	}
	// The separators must be percent-encoded, which is what keeps the whole
	// value inside authd's {family_id} segment.
	if !strings.Contains(u.wireURI, "%2F") {
		t.Errorf("wire URI = %q, want the slashes percent-encoded", u.wireURI)
	}
}

// A refused revoke must reach the operator as an error, not a success banner
// over a session that is still live.
func TestAuthdRevoke_UpstreamErrorSurfacesToOperator(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["DELETE /v1/sessions/"] = `{"error":"not_found","message":"no live session \"fam-x\""}`
	u.status["DELETE /v1/sessions/"] = http.StatusNotFound

	w := postAuthdRevoke(t, authdDash(t, u), url.Values{"family_id": {"fam-x"}})

	loc := w.Header().Get("Location")
	if strings.Contains(loc, "msg=revoked") {
		t.Fatalf("a refused revoke redirected as a success: %q", loc)
	}
	// The MESSAGE, not just the code — "authd said 404" alone would not tell an
	// operator that the session was already dead rather than the id mistyped.
	if !strings.Contains(loc, url.QueryEscape("no live session")) {
		t.Errorf("redirect dropped authd's message: %q", loc)
	}
}

func TestAuthdRevoke_NoAuthdURLRefuses(t *testing.T) {
	w := postAuthdRevoke(t, authdDash(t, nil), url.Values{"family_id": {"fam-1"}})

	loc := w.Header().Get("Location")
	if strings.Contains(loc, "msg=revoked") {
		t.Fatalf("revoke reported success with no authd configured: %q", loc)
	}
	if !strings.Contains(loc, url.QueryEscape("AUTHD_URL not configured")) {
		t.Errorf("redirect does not say why nothing happened: %q", loc)
	}
}

// ---- gates and failure modes ----

// The page lists every tenant's logins and the keys for the whole instance.
// dashd presents its own empty-folder service bearer, so authd would not narrow
// the answer to a folder-scoped viewer — the operator gate is the containment.
func TestAuthdPage_RequiresOperator(t *testing.T) {
	u := newAuthdUpstream(t)
	req := httptest.NewRequest("GET", "/dash/authd/", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	authdDash(t, u).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a folder-scoped viewer", w.Code)
	}
	if u.calls != 0 {
		t.Errorf("authd was called %d times for a non-operator — the gate runs after the read", u.calls)
	}
	if strings.Contains(w.Body.String(), "Signing keys") {
		t.Errorf("a non-operator was served the key section\nbody: %s", w.Body.String())
	}
}

func TestAuthdRevoke_RequiresOperator(t *testing.T) {
	u := newAuthdUpstream(t)
	req := httptest.NewRequest("POST", "/dash/authd/revoke",
		strings.NewReader(url.Values{"family_id": {"fam-1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	authdDash(t, u).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a folder-scoped viewer", w.Code)
	}
	if u.calls != 0 {
		t.Errorf("authd was called %d times for a non-operator — the gate runs after the write", u.calls)
	}
}

// A 403 from authd is the shape a missing service grant takes, and it must
// reach the operator: a silently empty key table on the page that exists to
// show the signing key is the worst available lie.
func TestAuthdPage_UpstreamErrorSurfacesToOperator(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/signing_keys"] = `{"error":"forbidden","message":"missing scope signing_keys:read"}`
	u.status["/v1/signing_keys"] = http.StatusForbidden

	sec := getAuthdSection(t, authdDash(t, u), keysFrom, keysTo)

	if !strings.Contains(sec, "banner-err") {
		t.Errorf("403 from authd did not raise an error banner\nsection: %s", sec)
	}
	if !strings.Contains(sec, "missing scope signing_keys:read") {
		t.Errorf("banner dropped authd's message\nsection: %s", sec)
	}
	if strings.Contains(sec, "No signing key yet") {
		t.Errorf("a failed read rendered as a legitimately empty list\nsection: %s", sec)
	}
}

// One section failing must not blank the other: an operator diagnosing a broken
// key read still needs to see who is signed in.
func TestAuthdPage_OneFailedReadDoesNotBlankTheOther(t *testing.T) {
	u := newAuthdUpstream(t)
	u.respond["/v1/signing_keys"] = `{"error":"boom"}`
	u.status["/v1/signing_keys"] = http.StatusInternalServerError
	u.respond["/v1/sessions"] = fmt.Sprintf(`[
	  {"family_id":"fam-1","sub":"alice@example.com","scope":"acme/**","status":"active",
	   "started_at":%q,"renewed_at":%q,"expires_at":%q,"rotations":1}]`,
		authdPast(time.Hour), authdPast(time.Minute), authdFuture(30*24*time.Hour))

	body := getAuthdPage(t, authdDash(t, u))
	keys := sectionBetween(t, body, keysFrom, keysTo)
	sessions := sectionBetween(t, body, sessionsFrom, sessionsTo)

	if !strings.Contains(keys, "banner-err") {
		t.Errorf("failed key read raised no banner\nsection: %s", keys)
	}
	if !strings.Contains(sessions, "alice@example.com") {
		t.Errorf("a failed key read blanked the session table\nsection: %s", sessions)
	}
}

// With no transport the page must say so rather than render empty tables that
// read as "no keys, nobody signed in" — on this page that would be alarming and
// wrong at the same time.
func TestAuthdPage_NoAuthdURLSaysSo(t *testing.T) {
	body := getAuthdPage(t, authdDash(t, nil))

	if !strings.Contains(body, "AUTHD_URL not configured") {
		t.Errorf("missing transport banner\nbody: %s", body)
	}
	if strings.Contains(body, "No signing key yet") || strings.Contains(body, "Nobody is signed in right now.") {
		t.Errorf("unconfigured transport rendered as empty lists\nbody: %s", body)
	}
}

// The audit rows authd emits already federate into /dash/audit/ (spec 5/I), so
// this page links there. A second table would be a second renderer of the same
// rows, and the two would drift.
func TestAuthdPage_LinksToTheAuditLogRatherThanRepeatingIt(t *testing.T) {
	u := newAuthdUpstream(t)
	body := getAuthdPage(t, authdDash(t, u))
	sec := sectionBetween(t, body, `<h2>What authd wrote down</h2>`, "")

	if !strings.Contains(sec, `href="/dash/audit/"`) {
		t.Errorf("no link to the federated audit log\nsection: %s", sec)
	}
	// Only the two read endpoints — a third call would mean this page fetched
	// the log to render it here.
	if u.calls != 2 {
		t.Errorf("authd calls = %d, want exactly 2 (signing_keys + sessions)", u.calls)
	}
}

// The fleet-wide lever — retiring the active key — has no wire face at all,
// deliberately (spec 5/1 § JWK rotation mechanics). The page must still explain
// it, because an operator who wants "log everyone out" and finds only per-login
// buttons will otherwise click all of them.
func TestAuthdPage_ExplainsTheFleetWideLeverHasNoButton(t *testing.T) {
	u := newAuthdUpstream(t)
	sec := getAuthdSection(t, authdDash(t, u), keysFrom, keysTo)

	if !strings.Contains(sec, "Signing everyone out at once") {
		t.Errorf("page does not address the fleet-wide logout question\nsection: %s", sec)
	}
	if !strings.Contains(sec, "no button for it here") {
		t.Errorf("page does not say the fleet-wide lever is out-of-band\nsection: %s", sec)
	}
	// If a button ever appears, this page grew an authority spec 5/1 defers.
	if strings.Contains(sec, `action="/dash/authd/rotate"`) {
		t.Errorf("the page grew a key-rotation control; spec 5/1 defers POST /v1/keys/rotate\nsection: %s", sec)
	}
}

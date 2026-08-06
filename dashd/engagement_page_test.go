package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	_ "modernc.org/sqlite"
)

// routdUpstream stands in for routd's /v1/engagement face. It records the
// request dashd sent, because WHICH credential dashd presents is half the
// contract: routd authorizes the bearer, not the X-User-* headers.
type routdUpstream struct {
	srv     *httptest.Server
	method  string
	path    string
	query   string
	body    string
	authz   string
	userSub string
	calls   int
	status  int
	respond string
}

func newRoutdUpstream(t *testing.T) *routdUpstream {
	t.Helper()
	u := &routdUpstream{status: 200, respond: `{"engaged":[]}`}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls++
		u.method, u.path, u.query = r.Method, r.URL.Path, r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		u.body = string(raw)
		u.authz = r.Header.Get("Authorization")
		u.userSub = r.Header.Get("X-User-Sub")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.status)
		io.WriteString(w, u.respond)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// engagementDash wires a dash pointed at the fake routd. dbRoutd is left NIL on
// purpose: this page must not touch the DB, and a nil handle turns any direct
// read into a panic the test would catch.
func engagementDash(t *testing.T, u *routdUpstream) *http.ServeMux {
	t.Helper()
	d := &dash{}
	if u != nil {
		d.routdURL = u.srv.URL
	}
	return newMux(d)
}

// getEngagementSection GETs the page as an operator and returns ONLY the
// live-window section. Whole-page Contains is worthless here: "engagement"
// appears in the nav link and the two intro paragraphs, so an assertion against
// the full body would pass with the table empty or absent.
func getEngagementSection(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/engagement/", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	return sectionBetween(t, body, `<h2>Engaged conversations</h2>`, "")
}

// future renders an RFC3339Nano deadline d from now, the shape routd emits.
func future(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339Nano)
}

func TestEngagementPage_RendersLiveWindows(t *testing.T) {
	u := newRoutdUpstream(t)
	// 28m30s, not 28m: remainingTS truncates, and the few microseconds spent
	// getting to the assertion would turn an exact 28m into "27m" now and then.
	until := future(28*time.Minute + 30*time.Second)
	u.respond = fmt.Sprintf(`{"engaged":[
		{"jid":"slack:C123","topic":"1712.55","folder":"corp/eng","engaged_until":%q}]}`, until)
	sec := getEngagementSection(t, engagementDash(t, u))

	if u.calls != 1 {
		t.Fatalf("routd calls = %d, want 1", u.calls)
	}
	if u.path != "/v1/engagement" {
		t.Errorf("routd path = %q, want /v1/engagement", u.path)
	}
	// The list form is the no-jid form; a jid would ask for a single pair.
	if u.query != "" {
		t.Errorf("routd query = %q, want empty (the list form takes no jid)", u.query)
	}
	for _, want := range []string{"slack:C123", "1712.55", "corp/eng", "28m", until} {
		if !strings.Contains(sec, want) {
			t.Errorf("window section missing %q\nsection: %s", want, sec)
		}
	}
	// The folder must be a link to its chat page, not bare text.
	if !strings.Contains(sec, `href="/dash/chat/corp%2Feng/"`) {
		t.Errorf("folder is not linked to its chat page\nsection: %s", sec)
	}
}

// A window on the root topic is the MAIN conversation, not a missing value. A
// blank cell would read as data dashd failed to load.
func TestEngagementPage_EmptyTopicNamesTheMainConversation(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = fmt.Sprintf(`{"engaged":[
		{"jid":"tg:42","topic":"","folder":"solo","engaged_until":%q}]}`, future(time.Hour))
	sec := getEngagementSection(t, engagementDash(t, u))

	if !strings.Contains(sec, "main conversation") {
		t.Errorf("empty topic did not render as the main conversation\nsection: %s", sec)
	}
}

func TestEngagementPage_EmptyState(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"engaged":[]}`
	sec := getEngagementSection(t, engagementDash(t, u))

	if !strings.Contains(sec, "No conversation is engaged right now.") {
		t.Errorf("missing empty state\nsection: %s", sec)
	}
	if strings.Contains(sec, "<table>") {
		t.Errorf("empty list rendered a table\nsection: %s", sec)
	}
}

// A non-2xx from routd must reach the operator as a banner, not a blank table.
// A logged-but-invisible failure is still silent.
func TestEngagementPage_UpstreamErrorSurfacesToOperator(t *testing.T) {
	u := newRoutdUpstream(t)
	u.status = http.StatusForbidden
	u.respond = `{"error":"forbidden","message":"missing scope routes:read"}`
	sec := getEngagementSection(t, engagementDash(t, u))

	if !strings.Contains(sec, "banner-err") {
		t.Errorf("403 from routd did not raise an error banner\nsection: %s", sec)
	}
	// The MESSAGE, not just the code — "routd said 403" alone would not tell an
	// operator that the service grant is the thing to fix.
	if !strings.Contains(sec, "missing scope routes:read") {
		t.Errorf("banner dropped routd's message\nsection: %s", sec)
	}
	if strings.Contains(sec, "No conversation is engaged right now.") {
		t.Errorf("a failed read rendered as a legitimately empty list\nsection: %s", sec)
	}
}

// The page must say so when the transport is unconfigured rather than render an
// empty table that looks like "nothing is engaged".
func TestEngagementPage_NoRouterURLSaysSo(t *testing.T) {
	sec := getEngagementSection(t, engagementDash(t, nil))

	if !strings.Contains(sec, "ROUTER_URL not configured") {
		t.Errorf("missing transport banner\nsection: %s", sec)
	}
	if strings.Contains(sec, "No conversation is engaged right now.") {
		t.Errorf("unconfigured transport rendered as an empty list\nsection: %s", sec)
	}
}

// dashd reads this page through routd's API, never out of dbRoutd — the direct-DB
// read is the recorded defect class. dbRoutd is nil in every test above, so a
// direct read would panic; this asserts the rows still render, which is what
// makes that nil meaningful rather than incidental.
func TestEngagementPage_ReadsRoutdNotTheDB(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = fmt.Sprintf(`{"engaged":[
		{"jid":"web:x","topic":"","folder":"x","engaged_until":%q}]}`, future(5*time.Minute))
	d := &dash{routdURL: u.srv.URL}
	if d.dbRoutd != nil {
		t.Fatal("this test is only meaningful with a nil dbRoutd")
	}
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/engagement/", nil)))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the page touched the nil DB handle", w.Code)
	}
	if !strings.Contains(w.Body.String(), "web:x") {
		t.Error("page rendered no window without a DB handle")
	}
}

// routd authorizes the BEARER, not the forwarded operator identity (that is
// proxyd's model). Forwarding X-User-* would imply routd narrows the answer per
// viewer, and it does not.
func TestEngagementPage_PresentsBearerAndNotUserHeaders(t *testing.T) {
	u := newRoutdUpstream(t)
	d := &dash{
		routdURL: u.srv.URL,
		svc:      func(context.Context) (string, error) { return "service-dashd-token", nil },
	}
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/engagement/", nil)))

	if u.calls != 1 {
		t.Fatalf("routd calls = %d, want 1", u.calls)
	}
	if u.authz != "Bearer service-dashd-token" {
		t.Errorf("Authorization = %q, want the service:dashd bearer", u.authz)
	}
	if u.userSub != "" {
		t.Errorf("dashd forwarded X-User-Sub=%q to routd, which authorizes the bearer instead", u.userSub)
	}
}

// The page is operator-only, like /dash/proactive/. It lists every tenant's
// windows (dashd's service token carries an empty folder claim, which routd
// reads as list-all), so a folder-scoped viewer must not reach it.
func TestEngagementPage_RequiresOperator(t *testing.T) {
	u := newRoutdUpstream(t)
	mux := engagementDash(t, u)

	req := httptest.NewRequest("GET", "/dash/engagement/", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a folder-scoped viewer", w.Code)
	}
	if u.calls != 0 {
		t.Errorf("routd was called %d times for a non-operator — the gate runs after the read", u.calls)
	}
}

// remainingTS exists because relativeTS measures time.Since, so a future
// deadline falls into its "now" arm. This pins BOTH halves: the trap is real,
// and remainingTS avoids it. Without the second half the helper could be a
// synonym for relativeTS and nothing would notice.
func TestRemainingTS_FutureDeadlineIsNotNow(t *testing.T) {
	in := future(28 * time.Minute)

	if got := relativeTS(in); got != "now" {
		t.Fatalf("relativeTS(future) = %q, want %q — the trap remainingTS exists for is gone, "+
			"so this guard no longer proves anything", got, "now")
	}
	if got := remainingTS(in); got != "27m" && got != "28m" {
		t.Errorf("remainingTS(+28m) = %q, want 27m or 28m", got)
	}
}

func TestRemainingTS_PastDeadlineIsExpired(t *testing.T) {
	if got := remainingTS(future(-time.Hour)); got != "expired" {
		t.Errorf("remainingTS(past) = %q, want expired", got)
	}
}

// ---- the disengage control (spec 5/G item 6, "view AND control") ----

// postDisengage submits the form the table renders, as an operator.
func postDisengage(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/dash/engagement/disengage", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(req))
	return w
}

// A runaway engagement — the agent replying in a chat nobody wants it in — had
// no operator remedy but waiting out the TTL. The control is destructive and
// visible to whoever is in that chat, so it is behind a confirm like every other
// dashd danger-zone action, never a bare button.
//
// Asserted against the window SECTION, not the page: the intro paragraph now
// says the word "disengage" too, so a whole-body Contains would pass with no
// control rendered at all.
func TestEngagementPage_OffersDisengageBehindConfirm(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = fmt.Sprintf(`{"engaged":[
		{"jid":"slack:C123","topic":"1712.55","folder":"corp/eng","engaged_until":%q}]}`, future(time.Hour))
	sec := getEngagementSection(t, engagementDash(t, u))

	if !strings.Contains(sec, `action="/dash/engagement/disengage"`) {
		t.Fatalf("no disengage control in the window table\nsection: %s", sec)
	}
	if !strings.Contains(sec, `onsubmit="return confirm(`) {
		t.Errorf("disengage is a bare button — no confirm step\nsection: %s", sec)
	}
	// The pair is the unit, and routd needs the claiming folder to contain the
	// write: a control that posted only the jid would clear the wrong topic.
	for _, want := range []string{
		`name="jid" value="slack:C123"`,
		`name="topic" value="1712.55"`,
		`name="folder" value="corp/eng"`,
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("disengage form missing %s\nsection: %s", want, sec)
		}
	}
}

// The control writes through routd's /v1, never dashd's own routd.db handle —
// routd owns the columns, applies the containment, and lands the audit row in
// the mutation's transaction. dbRoutd is nil here, so a direct write panics.
func TestEngagementDisengage_WritesThroughRoutdWithTTLZero(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"ok":true}`
	d := &dash{
		routdURL: u.srv.URL,
		svc:      func(context.Context) (string, error) { return "service-dashd-token", nil },
	}
	if d.dbRoutd != nil {
		t.Fatal("this test is only meaningful with a nil dbRoutd")
	}
	w := postDisengage(t, newMux(d), url.Values{
		"jid": {"slack:C123"}, "topic": {"1712.55"}, "folder": {"corp/eng"},
	})

	if u.calls != 1 {
		t.Fatalf("routd calls = %d, want 1", u.calls)
	}
	if u.method != http.MethodPost || u.path != "/v1/engagement" {
		t.Errorf("routd got %s %s, want POST /v1/engagement", u.method, u.path)
	}
	var got apiv1.EngagementRequest
	if err := json.Unmarshal([]byte(u.body), &got); err != nil {
		t.Fatalf("routd body %q is not an EngagementRequest: %v", u.body, err)
	}
	// ttl_seconds 0 IS the disengage path; any positive value would EXTEND the
	// window this control exists to end.
	if got.TTLSeconds != 0 {
		t.Errorf("ttl_seconds = %d, want 0 — a positive TTL extends the window", got.TTLSeconds)
	}
	if got.JID != "slack:C123" || got.Topic != "1712.55" || got.Folder != "corp/eng" {
		t.Errorf("routd got %+v, want the (jid, topic) pair and its claiming folder", got)
	}
	if u.authz != "Bearer service-dashd-token" {
		t.Errorf("Authorization = %q, want the service:dashd bearer", u.authz)
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 back to the page", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/dash/engagement/?msg=disengaged" {
		t.Errorf("Location = %q, want /dash/engagement/?msg=disengaged", loc)
	}
}

// Operator-only, like the view. A folder-scoped viewer must not reach routd at
// all: dashd presents its own empty-folder service bearer, so routd would
// authorize the write as the instance root rather than as the viewer.
func TestEngagementDisengage_RequiresOperator(t *testing.T) {
	u := newRoutdUpstream(t)
	req := httptest.NewRequest("POST", "/dash/engagement/disengage",
		strings.NewReader(url.Values{"jid": {"slack:C123"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	newMux(&dash{routdURL: u.srv.URL}).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a folder-scoped viewer", w.Code)
	}
	if u.calls != 0 {
		t.Errorf("routd was called %d times for a non-operator — the gate runs after the write", u.calls)
	}
}

// A refused write must reach the operator as an error, not as a success banner
// over a window that is still live. A logged-but-invisible failure is still
// silent, and here it would be actively misleading.
func TestEngagementDisengage_UpstreamErrorSurfacesToOperator(t *testing.T) {
	u := newRoutdUpstream(t)
	u.status = http.StatusForbidden
	u.respond = `{"error":"forbidden","message":"missing scope routes:write"}`
	w := postDisengage(t, engagementDash(t, u), url.Values{"jid": {"slack:C123"}})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if strings.Contains(loc, "msg=disengaged") {
		t.Fatalf("a refused write redirected as a success: %q", loc)
	}
	// The MESSAGE, not just the code — "routd said 403" alone would not tell an
	// operator that the service grant is the thing to fix.
	if !strings.Contains(loc, url.QueryEscape("missing scope routes:write")) {
		t.Errorf("redirect dropped routd's message: %q", loc)
	}
}

// With no transport the control must refuse loudly. Redirecting to a bare
// success would tell the operator the agent had stopped listening when nothing
// was sent anywhere.
func TestEngagementDisengage_NoRouterURLRefuses(t *testing.T) {
	w := postDisengage(t, engagementDash(t, nil), url.Values{"jid": {"slack:C123"}})

	loc := w.Header().Get("Location")
	if strings.Contains(loc, "msg=disengaged") {
		t.Fatalf("disengage reported success with no routd configured: %q", loc)
	}
	if !strings.Contains(loc, url.QueryEscape("ROUTER_URL not configured")) {
		t.Errorf("redirect does not say why nothing happened: %q", loc)
	}
}

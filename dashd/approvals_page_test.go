package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// approvalsDash wires a dash pointed at the fake routd. The handle stays NIL: the
// page must resolve everything over HTTP, so a direct read panics the test.
func approvalsDash(t *testing.T, u *routdUpstream) *http.ServeMux {
	t.Helper()
	d := &dash{}
	if u != nil {
		d.routdURL = u.srv.URL
	}
	return newMux(d)
}

func TestApprovalsPage_RendersHeldQueue(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"ab12","group_folder":"corp/eng","tool":"send_file","args":"{\"path\":\"/etc/x\"}",
		 "status":"held","chat_jid":"tg:9","created_at":"2026-08-08T06:00:00Z"},
		{"id":"cd34","group_folder":"solo","tool":"post","status":"rejected",
		 "reviewed_by":"google:op@x","reviewed_at":"2026-08-07T10:00:00Z","reviewer_note":"too wide",
		 "created_at":"2026-08-07T09:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if u.path != "/v1/pending_actions" {
		t.Errorf("routd path = %q, want /v1/pending_actions", u.path)
	}
	body := w.Body.String()

	held := sectionBetween(t, body, `<h2>Waiting for a verdict</h2>`, `<h2>Recent verdicts</h2>`)
	for _, want := range []string{
		"corp/eng", "send_file", `/etc/x`,
		`action="/dash/approvals/ab12/resolve"`, `value="approve"`, `value="reject"`,
	} {
		if !strings.Contains(held, want) {
			t.Errorf("held section missing %q\nsection: %s", want, held)
		}
	}

	resolved := sectionBetween(t, body, `<h2>Recent verdicts</h2>`, "")
	for _, want := range []string{"solo", "rejected", "google:op@x", "too wide"} {
		if !strings.Contains(resolved, want) {
			t.Errorf("resolved section missing %q\nsection: %s", want, resolved)
		}
	}
	// A resolved row must carry no verdict form — the decision is made.
	if strings.Contains(resolved, "/resolve") {
		t.Error("resolved section renders a verdict form")
	}
}

// The queue a caller reads is bounded by WHO they are, and dashd says who that
// is: the proxyd-verified sub rides as `reviewer` and routd returns only the
// folders that human holds admin on. A page that asked routd without it would
// render every tenant's held calls.
func TestApprovalsPage_ForwardsTheReviewer(t *testing.T) {
	u := newRoutdUpstream(t)
	mux := approvalsDash(t, u)

	req := httptest.NewRequest("GET", "/dash/approvals/", nil)
	req.Header.Set("X-User-Sub", "user:google:someone")
	req.Header.Set("X-User-Groups", `["solo"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// The bare sub, the spelling acl rows carry — a `user:`-prefixed principal
	// matches no row and would return an empty queue to a real admin.
	if got := u.query; got != "reviewer=google%3Asomeone" {
		t.Fatalf("routd query = %q, want the bare reviewer sub", got)
	}
}

// An unauthenticated request never reaches routd: there is no human to filter
// the queue for, so asking would be asking for everything.
func TestApprovalsPage_AnonymousMakesNoUpstreamCall(t *testing.T) {
	u := newRoutdUpstream(t)
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/dash/approvals/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if u.calls != 0 {
		t.Fatalf("routd calls = %d, want 0", u.calls)
	}
}

func TestApprovalResolve_ForwardsVerdictNoteAndReviewer(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"id":"ab12","status":"approved"}`
	mux := approvalsDash(t, u)

	form := url.Values{"verdict": {"approve"}, "note": {"looks fine"}}
	req := asOperator(httptest.NewRequest("POST", "/dash/approvals/ab12/resolve",
		strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/dash/approvals/?msg=approved" {
		t.Errorf("redirect = %q", got)
	}
	if u.method != "POST" || u.path != "/v1/pending_actions/ab12/approve" {
		t.Errorf("routd call = %s %s, want POST /v1/pending_actions/ab12/approve", u.method, u.path)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(u.body), &sent); err != nil {
		t.Fatalf("body %q: %v", u.body, err)
	}
	// reviewed_by must name the human operator, not dashd's service principal.
	if sent["reviewer"] != "op@x" || sent["note"] != "looks fine" {
		t.Errorf("forwarded body = %v", sent)
	}
}

// routd's refusal (double verdict → 409) surfaces on the page, not as a blank
// redirect: the operator must learn the verdict did NOT land.
func TestApprovalResolve_UpstreamErrorSurfaces(t *testing.T) {
	u := newRoutdUpstream(t)
	u.status = http.StatusConflict
	u.respond = `{"error":"conflict","message":"pending action \"ab12\" already resolved (approved)"}`
	mux := approvalsDash(t, u)

	form := url.Values{"verdict": {"reject"}}
	req := asOperator(httptest.NewRequest("POST", "/dash/approvals/ab12/resolve",
		strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") || !strings.Contains(loc, "already+resolved") {
		t.Errorf("redirect %q does not carry routd's reason", loc)
	}
}

func TestApprovalResolve_BadVerdictRejected(t *testing.T) {
	u := newRoutdUpstream(t)
	mux := approvalsDash(t, u)

	form := url.Values{"verdict": {"maybe"}}
	req := asOperator(httptest.NewRequest("POST", "/dash/approvals/ab12/resolve",
		strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if u.calls != 0 {
		t.Fatalf("routd calls = %d, want 0", u.calls)
	}
}

// pendingCountDB is a migrated routd.db; the portal count reads pending_actions.
func pendingCountDB(t *testing.T) *dash {
	t.Helper()
	return &dash{routd: routdDB(t)}
}

// The portal banner counts only calls still WAITING that this caller may
// ANSWER: a lazily-expired hold and a resolved row ask nobody for anything, and
// a call in a folder the caller holds no admin on is not theirs to answer.
func TestPortalCountsHeldApprovals(t *testing.T) {
	d := pendingCountDB(t)
	seedAdmin(t, d.routd, "op@x", "**")
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, row := range [][3]string{
		{"p1", "held", ""},
		{"p2", "held", past},
		{"p3", "rejected", ""},
	} {
		// routd writes these items, so the fixture addresses them the way
		// routd does — the id is the coordinate, and an empty expires_at is
		// left OFF, which is what "waits forever" means to the reader.
		attrs := map[string]string{
			store.AttrPendGroupFolder: "demo",
			store.AttrPendCallerAgent: "agent:demo",
			store.AttrPendTool:        "send_file",
			store.AttrPendStatus:      row[1],
			store.AttrPendCreatedAt:   "2026-08-09T00:00:00Z",
		}
		if row[2] != "" {
			attrs[store.AttrPendExpiresAt] = row[2]
		}
		if _, err := d.routd.GuardedBatch(t.Context(), nil,
			[]objstore.Write{{
				Op:     objstore.Put,
				Entity: store.PendingActions,
				Key:    objstore.Key{Partition: store.OwnerRoutd, Sort: row[0]},
				Attrs:  attrs,
			}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := d.countHeldApprovals(asOperator(httptest.NewRequest("GET", "/dash/", nil))); n != 1 {
		t.Fatalf("countHeldApprovals = %d, want 1 (expired + resolved excluded)", n)
	}
	// A signed-in caller holding nothing on `demo` is asked for no verdict.
	stranger := httptest.NewRequest("GET", "/dash/", nil)
	stranger.Header.Set("X-User-Sub", "nobody@x")
	if n := d.countHeldApprovals(stranger); n != 0 {
		t.Fatalf("countHeldApprovals for a stranger = %d, want 0", n)
	}

	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("portal status = %d", w.Code)
	}
	want := fmt.Sprintf(`<a href="/dash/approvals/">%d tool call held for your approval</a>`, 1)
	if !strings.Contains(w.Body.String(), want) {
		t.Errorf("portal missing banner %q", want)
	}
}

// The deep link routd puts in the held notice (spec 6/26 § Gap 3) names WHICH
// call. The page moves it to the top of the queue and marks it, so the operator
// does not read a table to find the row they were sent to.
func TestApprovalsPage_DeepLinkFocusesRow(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"aa11","group_folder":"corp/eng","tool":"send_file","status":"held",
		 "created_at":"2026-08-08T06:00:00Z"},
		{"id":"bb22","group_folder":"solo","tool":"post","status":"held",
		 "created_at":"2026-08-08T05:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/?id=bb22", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	held := sectionBetween(t, w.Body.String(), `<h2>Waiting for a verdict</h2>`, "")
	first, second := strings.Index(held, "solo"), strings.Index(held, "corp/eng")
	if first < 0 || second < 0 || first > second {
		t.Errorf("focused row not first: solo at %d, corp/eng at %d\n%s", first, second, held)
	}
	if !strings.Contains(held, "&#9654;") {
		t.Error("focused row carries no marker")
	}
}

// An operator who follows the link after someone else voted must be TOLD the
// verdict is in, not shown a queue that silently lacks the row.
func TestApprovalsPage_DeepLinkToResolvedRowSaysSo(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"cd34","group_folder":"solo","tool":"post","status":"approved",
		 "reviewed_by":"google:op@x","created_at":"2026-08-07T09:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/?id=cd34", nil)))
	if body := w.Body.String(); !strings.Contains(body, "already has a verdict") {
		t.Errorf("resolved deep link says nothing:\n%s", body)
	}
}

// An expired or unknown id is an error the operator sees, not a blank queue.
func TestApprovalsPage_DeepLinkToUnknownRowReports(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/?id=zz99", nil)))
	if body := w.Body.String(); !strings.Contains(body, "no held call with id zz99") {
		t.Errorf("unknown deep link says nothing:\n%s", body)
	}
}

// The `always` button is approve PLUS routd's `standing` argument — not a
// fourth endpoint, because pending_actions has no third status (spec 6/26
// Decision 5).
func TestApprovalResolve_AlwaysSendsStandingOnApprove(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"id":"ab12","status":"approved"}`
	mux := approvalsDash(t, u)

	form := url.Values{"verdict": {"always"}}
	req := asOperator(httptest.NewRequest("POST", "/dash/approvals/ab12/resolve",
		strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %s)", w.Code, w.Body.String())
	}
	if u.path != "/v1/pending_actions/ab12/approve" {
		t.Errorf("routd path = %q, want the approve action", u.path)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(u.body), &sent); err != nil {
		t.Fatalf("body %q: %v", u.body, err)
	}
	if sent["standing"] != "true" {
		t.Errorf("forwarded body = %v, want standing=true", sent)
	}
	if got := w.Header().Get("Location"); got != "/dash/approvals/?msg=always" {
		t.Errorf("redirect = %q; the operator must be told a rule was written", got)
	}
}

// The page prints the rule the `always` button will write — routd's `matcher`,
// never one rendered here — plus the two properties that widen it: a rule
// constrains only the arguments it NAMES, and no rule can constrain a
// non-scalar argument (spec 6/26 § "The predicate is part of the verdict").
func TestApprovalsPage_ShowsTheRuleAlwaysWouldWrite(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"ab12","group_folder":"solo","tool":"create_pr","args":"{}","status":"held",
		 "matcher":"repo=kronael/arizuko","unmatchable":["files"],
		 "created_at":"2026-08-08T06:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/", nil)))
	body := w.Body.String()

	if !strings.Contains(body, `value="always"`) {
		t.Error("a statable rule must offer the always button")
	}
	if !strings.Contains(body, "create_pr(repo=kronael/arizuko)") {
		t.Errorf("the page must print the exact rule:\n%s", body)
	}
	if !strings.Contains(body, "Arguments it does not name are unconstrained") {
		t.Errorf("the page must state the names-only widening:\n%s", body)
	}
	if !strings.Contains(body, "No rule can constrain files") {
		t.Errorf("the page must name the unmatchable argument:\n%s", body)
	}
}

// A rule routd cannot state exactly gets no button and a reason. Offering one
// would promise a standing answer the writer then refuses.
func TestApprovalsPage_NoAlwaysButtonWhenNoRuleCanBeWritten(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"ab12","group_folder":"solo","tool":"delete","args":"{}","status":"held",
		 "matcher_error":"argument \"target\": its value cannot be written as an exact rule",
		 "created_at":"2026-08-08T06:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/", nil)))
	body := w.Body.String()

	if strings.Contains(body, `value="always"`) {
		t.Error("a rule that cannot be stated must not be offered")
	}
	if !strings.Contains(body, "no standing answer for this call") {
		t.Errorf("the page must say why the button is missing:\n%s", body)
	}
}

// A held call with no scalar arguments yields an UNNARROWED rule. That is
// honest but large, so the page says so rather than letting "always" read as
// "always this one call".
func TestApprovalsPage_UnnarrowedRuleSaysSo(t *testing.T) {
	u := newRoutdUpstream(t)
	u.respond = `{"pending":[
		{"id":"ab12","group_folder":"solo","tool":"wipe","args":"{}","status":"held",
		 "created_at":"2026-08-08T06:00:00Z"}]}`
	mux := approvalsDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/approvals/", nil)))
	if body := w.Body.String(); !strings.Contains(body, "EVERY call to this tool will run") {
		t.Errorf("an unnarrowed rule must be described as one:\n%s", body)
	}
}

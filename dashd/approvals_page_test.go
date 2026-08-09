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

	_ "modernc.org/sqlite"
)

// approvalsDash wires a dash pointed at the fake routd. dbRoutd stays NIL: the
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

// A non-operator gets 403 before dashd makes any upstream call: routd
// authorizes dashd's bearer as list-all, so the page gate IS the containment.
func TestApprovalsPage_NonOperatorMakesNoUpstreamCall(t *testing.T) {
	u := newRoutdUpstream(t)
	mux := approvalsDash(t, u)

	req := httptest.NewRequest("GET", "/dash/approvals/", nil)
	req.Header.Set("X-User-Sub", "google:someone")
	req.Header.Set("X-User-Groups", `["solo"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
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
	return &dash{dbRoutd: routdDB(t)}
}

// The portal banner counts only calls still WAITING: a lazily-expired hold and
// a resolved row are not asking the operator for anything.
func TestPortalCountsHeldApprovals(t *testing.T) {
	d := pendingCountDB(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, row := range [][3]string{
		{"p1", "held", ""},
		{"p2", "held", past},
		{"p3", "rejected", ""},
	} {
		if _, err := d.dbRoutd.Exec(
			`INSERT INTO pending_actions (id, group_folder, caller_agent, tool, status, created_at, expires_at)
			 VALUES (?, 'demo', 'agent:demo', 'send_file', ?, '2026-08-09T00:00:00Z', ?)`,
			row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	if n := d.countHeldApprovals(); n != 1 {
		t.Fatalf("countHeldApprovals = %d, want 1 (expired + resolved excluded)", n)
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

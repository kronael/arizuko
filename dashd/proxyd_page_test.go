package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// proxydUpstream stands in for proxyd's /v1/proxyd_routes face. It records what
// dashd sent — method, path, body and the forwarded identity headers — because
// those headers are what makes proxyd's in-tx audit row name the operator.
type proxydUpstream struct {
	srv     *httptest.Server
	method  string
	path    string
	body    string
	sub     string
	groups  string
	authz   string
	calls   int
	status  int
	respond string
}

func newProxydUpstream(t *testing.T) *proxydUpstream {
	t.Helper()
	u := &proxydUpstream{status: 200, respond: `{"routes":[]}`}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		u.calls++
		u.method, u.path, u.body = r.Method, r.URL.Path, string(buf)
		u.sub = r.Header.Get("X-User-Sub")
		u.groups = r.Header.Get("X-User-Groups")
		u.authz = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.status)
		io.WriteString(w, u.respond)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// proxydDash wires a dash pointed at the fake proxyd.
func proxydDash(t *testing.T, u *proxydUpstream) (*dash, *http.ServeMux) {
	t.Helper()
	db := routdDB(t)
	t.Cleanup(func() { db.Close() })
	d := &dash{dbRoutd: db}
	if u != nil {
		d.proxydURL = u.srv.URL
	}
	return d, newMux(d)
}

func TestProxydPage_RendersRoutes(t *testing.T) {
	u := newProxydUpstream(t)
	u.respond = `{"routes":[
		{"path":"/slack/","backend":"http://slakd:8080","auth":"public","preserve_headers":["X-Slack-Signature"]},
		{"path":"/dash/","backend":"http://dashd:8080","auth":"user"},
		{"path":"/lore","redirect_to":"/pub/krons/lore","auth":"public"}]}`
	_, mux := proxydDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/proxyd/", nil)))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if u.path != "/v1/proxyd_routes" || u.method != http.MethodGet {
		t.Errorf("upstream call = %s %s, want GET /v1/proxyd_routes", u.method, u.path)
	}
	for _, want := range []string{"/slack/", "http://slakd:8080", "/dash/", "http://dashd:8080"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The `auth` field is spelled out, not dumped raw — the 13yo test.
	if !strings.Contains(body, "anyone") || !strings.Contains(body, "signed-in users") {
		t.Errorf("auth values not spelled out for a human: %s", body)
	}
	// A redirect route shows its destination, not an empty backend cell.
	if !strings.Contains(body, "/pub/krons/lore") {
		t.Errorf("redirect target missing: %s", body)
	}
	if !strings.Contains(body, `action="/dash/proxyd/delete"`) {
		t.Errorf("missing per-route delete control")
	}
	// Delete is destructive: it must be behind a confirm, not a bare button.
	if !strings.Contains(body, "onsubmit=\"return confirm(") {
		t.Errorf("delete control has no confirm step: %s", body)
	}
	if !strings.Contains(body, "Add route") {
		t.Errorf("missing add form")
	}
}

// A dashd with no PROXYD_URL says so instead of rendering an empty table that
// reads as "proxyd has no routes".
func TestProxydPage_NoURL(t *testing.T) {
	_, mux := proxydDash(t, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/proxyd/", nil)))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "PROXYD_URL is not configured") {
		t.Errorf("missing unconfigured banner: %s", body)
	}
	if strings.Contains(body, "Add route") {
		t.Errorf("add form offered with no reachable proxyd: %s", body)
	}
}

// proxyd's ACL refusing the caller must reach the operator as an explanation,
// not an empty page. Fail loud, fail to the user.
func TestProxydPage_UpstreamForbiddenSurfaces(t *testing.T) {
	u := newProxydUpstream(t)
	u.status = http.StatusForbidden
	u.respond = `{"error":"forbidden"}`
	_, mux := proxydDash(t, u)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/proxyd/", nil)))
	body := w.Body.String()
	if !strings.Contains(body, "banner-err") || !strings.Contains(body, "operator grant") {
		t.Errorf("403 from proxyd not explained to the operator: %s", body)
	}
	if strings.Contains(body, "Add route") {
		t.Errorf("add form offered after a failed read: %s", body)
	}
}

func TestProxydPage_NonOperatorForbidden(t *testing.T) {
	u := newProxydUpstream(t)
	_, mux := proxydDash(t, u)

	req := httptest.NewRequest("GET", "/dash/proxyd/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// Containment: a folder-scoped caller must not even reach proxyd. A gate
	// that 403s the browser but still forwarded the call would leak the table.
	if u.calls != 0 {
		t.Errorf("non-operator reached proxyd (%d calls)", u.calls)
	}
}

func TestProxydRouteCreate_CallsUpstream(t *testing.T) {
	u := newProxydUpstream(t)
	u.status = http.StatusCreated
	u.respond = `{"path":"/myapp/","backend":"http://myapp:8080","auth":"user"}`
	_, mux := proxydDash(t, u)

	form := url.Values{
		"path": {"/myapp/"}, "backend": {"http://myapp:8080"},
		"auth": {"operator"}, "strip_prefix": {"1"},
	}.Encode()
	req := asOperator(httptest.NewRequest("POST", "/dash/proxyd/", strings.NewReader(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%q", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "msg=added") {
		t.Errorf("redirect = %q, want the added banner", loc)
	}
	if u.method != http.MethodPost || u.path != "/v1/proxyd_routes" {
		t.Fatalf("upstream call = %s %s, want POST /v1/proxyd_routes", u.method, u.path)
	}
	var sent proxydRoute
	if err := json.Unmarshal([]byte(u.body), &sent); err != nil {
		t.Fatalf("upstream body %q: %v", u.body, err)
	}
	if sent.Path != "/myapp/" || sent.Backend != "http://myapp:8080" ||
		sent.Auth != "operator" || !sent.StripPrefix {
		t.Errorf("upstream route = %+v, want /myapp/ → http://myapp:8080, operator, strip", sent)
	}
	// The forwarded identity is what proxyd records as the audit actor.
	if u.sub != "op@x" {
		t.Errorf("X-User-Sub forwarded = %q, want op@x (proxyd audits the stamped sub)", u.sub)
	}
	if !strings.Contains(u.groups, "**") {
		t.Errorf("X-User-Groups forwarded = %q, want the operator marker", u.groups)
	}
}

// An upstream refusal must land in front of the operator, not in the log only.
func TestProxydRouteCreate_UpstreamErrorSurfaces(t *testing.T) {
	u := newProxydUpstream(t)
	u.status = http.StatusConflict
	u.respond = `{"error":"route /myapp/ already exists"}`
	_, mux := proxydDash(t, u)

	form := url.Values{"path": {"/myapp/"}, "backend": {"http://myapp:8080"}, "auth": {"user"}}.Encode()
	req := asOperator(httptest.NewRequest("POST", "/dash/proxyd/", strings.NewReader(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Fatalf("redirect = %q, want the failure surfaced", loc)
	}
	if !strings.Contains(loc, url.QueryEscape("already exists")) {
		t.Errorf("redirect = %q, want proxyd's reason carried through", loc)
	}
	if strings.Contains(loc, "msg=added") {
		t.Errorf("failed create reported as success: %q", loc)
	}
}

func TestProxydRouteDelete_CallsUpstream(t *testing.T) {
	u := newProxydUpstream(t)
	u.status = http.StatusNoContent
	u.respond = ""
	_, mux := proxydDash(t, u)

	form := url.Values{"path": {"/slack/events"}}.Encode()
	req := asOperator(httptest.NewRequest("POST", "/dash/proxyd/delete", strings.NewReader(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%q", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "msg=deleted") {
		t.Errorf("redirect = %q, want the deleted banner", loc)
	}
	if u.method != http.MethodDelete {
		t.Errorf("upstream method = %s, want DELETE", u.method)
	}
	// The path is a URL path segment containing slashes; it must arrive whole.
	if u.path != "/v1/proxyd_routes/%2Fslack%2Fevents" && u.path != "/v1/proxyd_routes//slack/events" {
		t.Errorf("upstream path = %q, want the escaped route path", u.path)
	}
	if u.sub != "op@x" {
		t.Errorf("X-User-Sub forwarded = %q, want op@x", u.sub)
	}
}

func TestProxydRouteMutations_NonOperatorForbidden(t *testing.T) {
	for _, tc := range []struct{ name, path, form string }{
		{"create", "/dash/proxyd/", "path=/x/&backend=http://x:8080&auth=public"},
		{"delete", "/dash/proxyd/delete", "path=/x/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newProxydUpstream(t)
			_, mux := proxydDash(t, u)
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-User-Sub", "github:regular")
			req.Header.Set("X-User-Groups", `["corp/eng"]`)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			if u.calls != 0 {
				t.Errorf("non-operator mutation reached proxyd (%d calls)", u.calls)
			}
		})
	}
}

// The proxyd tile links into the control plane now that it exists.
func TestProxydTileBuilt(t *testing.T) {
	for _, s := range services {
		if s.Name == "proxyd" {
			if !s.Built {
				t.Fatalf("proxyd tile Built=false but /dash/proxyd/ ships")
			}
			if !shouldLink(s) {
				t.Errorf("proxyd tile does not link to %s", s.Dash)
			}
			return
		}
	}
	t.Fatal("no proxyd entry in services")
}

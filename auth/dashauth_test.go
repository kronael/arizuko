package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// req builds a dashboard request with the proxyd-stamped scopes header.
func req(groupsJSON string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://dash.example/dash/routd/", nil)
	if groupsJSON != "" {
		r.Header.Set("X-User-Groups", groupsJSON)
	}
	return r
}

// A malformed or missing scopes header must read as NO scopes. Reading it as
// all scopes is the shape that turns a header parse bug into an operator
// takeover.
func TestCallerGroupsFailsClosed(t *testing.T) {
	for name, hdr := range map[string]string{
		"absent":     "",
		"malformed":  `["**"`,
		"not a list": `{"scope":"**"}`,
		"a bare str": `"**"`,
	} {
		if got := CallerGroups(req(hdr)); len(got) != 0 {
			t.Errorf("%s: CallerGroups = %v, want none", name, got)
		}
		if IsOperator(req(hdr)) {
			t.Errorf("%s: read as operator", name)
		}
	}
	if !IsOperator(req(`["**"]`)) {
		t.Error(`["**"] is the operator scope and was refused`)
	}
	if IsOperator(req(`["atlas","atlas/**"]`)) {
		t.Error("a folder-scoped caller read as operator")
	}
}

// The refusal is a themed page, not a bare string: 6/28 requires the error
// banner so an operator who lands on a 403 still sees where they are. onbod's
// copy of this gate rendered "forbidden" and timed's rendered the page — the
// drift this helper exists to end.
func TestRequireOperatorRefusesWithTheThemedPage(t *testing.T) {
	ks := &KeySet{}
	w := httptest.NewRecorder()
	if RequireOperator(w, req(`["atlas"]`), ks, "routd") {
		t.Fatal("a folder-scoped caller passed the operator gate")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "banner-err") {
		t.Error("the refusal is not the themed error banner")
	}
	if !strings.Contains(body, "routd") {
		t.Error("the refusal does not name the daemon it came from")
	}

	ok := httptest.NewRecorder()
	if !RequireOperator(ok, req(`["**"]`), ks, "routd") {
		t.Error("an operator was refused")
	}
	if ok.Body.Len() != 0 {
		t.Error("the gate wrote a body on the pass path")
	}
}

// A nil KeySet is local dev with no authd to verify a transit proof against.
// Every /v1 gate in the tree falls open there; the dashboard must not be the
// one surface that hard-fails without authd.
func TestNilKeySetFallsOpen(t *testing.T) {
	w := httptest.NewRecorder()
	if !RequireOperator(w, req(""), nil, "routd") {
		t.Error("nil ks refused; local dev has no authd to check against")
	}
	if !RequireWrite(w, req(""), nil, "routd") {
		t.Error("nil ks refused a write")
	}
}

// A dashboard POST is a cookie-authenticated write, so a third-party page must
// not be able to drive an operator's browser into one. Neither daemon copy of
// this gate had any CSRF check at all.
func TestRequireWriteRefusesCrossOrigin(t *testing.T) {
	ks := &KeySet{}
	cases := []struct {
		name, header, value string
		want                bool
	}{
		{"same-origin Origin", "Origin", "http://dash.example", true},
		{"same-origin Referer", "Referer", "http://dash.example/dash/routd/", true},
		{"foreign Origin", "Origin", "http://evil.example", false},
		{"foreign Referer", "Referer", "http://evil.example/x", false},
		{"neither header", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://dash.example/dash/routd/x", nil)
		r.Header.Set("X-User-Groups", `["**"]`)
		if c.header != "" {
			r.Header.Set(c.header, c.value)
		}
		w := httptest.NewRecorder()
		if got := RequireWrite(w, r, ks, "routd"); got != c.want {
			t.Errorf("%s: RequireWrite = %v, want %v", c.name, got, c.want)
		}
	}
}

// Operator-first ordering: a non-operator is refused for lacking scope, and
// never learns whether the origin check would have passed.
func TestWriteChecksOperatorBeforeOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://dash.example/dash/routd/x", nil)
	r.Header.Set("X-User-Groups", `["atlas"]`)
	r.Header.Set("Origin", "http://dash.example")
	w := httptest.NewRecorder()
	if RequireWrite(w, r, &KeySet{}, "routd") {
		t.Fatal("a non-operator passed the write gate")
	}
	if !strings.Contains(w.Body.String(), "Operator access required") {
		t.Error("the refusal named the origin, not the missing scope")
	}
}

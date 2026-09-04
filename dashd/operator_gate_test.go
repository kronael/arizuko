package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
)

// TestOperatorGate_RefusesWithTheThemedBanner — spec 6/28 requires a deep-page
// 403 to render the theme error banner. dashd answered `http.Error` bare text,
// which is the only dashboard in the fleet that did (BUGS L141).
func TestOperatorGate_RefusesWithTheThemedBanner(t *testing.T) {
	d, _ := splitAdminDash(t, "user:someone-else")
	for _, c := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request) bool
	}{
		{"requireOperator", func(w http.ResponseWriter, r *http.Request) bool { return d.requireOperator(w, r) }},
		{"requireVisible", func(w http.ResponseWriter, r *http.Request) bool { return d.requireVisible(w, r, "someone-elses") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/dash/", nil)
			r.Header.Set("X-User-Sub", "user:nobody")
			r.Header.Set("X-User-Groups", `["tenant-a"]`)
			w := httptest.NewRecorder()

			if c.call(w, r) {
				t.Fatal("a non-operator must not pass the gate")
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want the themed HTML page", ct)
			}
			if body := w.Body.String(); !strings.Contains(body, "banner-err") {
				t.Errorf("refusal is not the theme banner:\n%s", body)
			}
		})
	}
}

// TestOperatorGate_ReadsTheSharedGroupSource — dashd carried a byte-for-byte
// copy of auth.CallerGroups and the literal "**" beside auth.OperatorGroup.
// Three copies of one operator decision is what 6/28 says the shared gate
// replaced; this pins dashd onto the shared readers.
func TestOperatorGate_ReadsTheSharedGroupSource(t *testing.T) {
	d := &dash{}
	r := httptest.NewRequest("GET", "/dash/", nil)
	r.Header.Set("X-User-Groups", `["`+auth.OperatorGroup+`"]`)

	allowed, operator := d.callerScope(r)
	if !operator {
		t.Fatalf("auth.OperatorGroup in the header must read as operator, got %v", allowed)
	}
	if len(allowed) != len(auth.CallerGroups(r)) {
		t.Errorf("callerScope = %v, auth.CallerGroups = %v — one reader, not two",
			allowed, auth.CallerGroups(r))
	}
}

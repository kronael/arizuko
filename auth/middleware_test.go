package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const mwSecret = "mw-test-secret"

// signedRequest stamps r with the canonical identity headers + a valid sig.
func signedRequest(r *http.Request, sub, name, groups string) {
	r.Header.Set("X-User-Sub", sub)
	r.Header.Set("X-User-Name", name)
	r.Header.Set("X-User-Groups", groups)
	r.Header.Set("X-User-Sig", SignHMAC(mwSecret, UserSigMessage(sub, name, groups)))
}

func TestRequireSigned_MissingSig_Redirects(t *testing.T) {
	called := false
	h := RequireSigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sub", "github:42") // claim only, no sig
	w := httptest.NewRecorder()

	h(w, r)

	if called {
		t.Fatal("next handler called despite missing sig")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Fatalf("redirect: got %q want /auth/login", loc)
	}
	for _, h := range identityHeaders {
		if r.Header.Get(h) != "" {
			t.Fatalf("identity header %q not stripped", h)
		}
	}
}

func TestRequireSigned_InvalidSig_Redirects(t *testing.T) {
	called := false
	h := RequireSigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sub", "github:42")
	r.Header.Set("X-User-Name", "alice")
	r.Header.Set("X-User-Groups", `["**"]`)
	r.Header.Set("X-User-Sig", "deadbeef") // bogus
	w := httptest.NewRecorder()

	h(w, r)

	if called {
		t.Fatal("next handler called despite invalid sig")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}
	for _, h := range identityHeaders {
		if r.Header.Get(h) != "" {
			t.Fatalf("identity header %q not stripped", h)
		}
	}
}

func TestRequireSigned_ValidSig_Passes(t *testing.T) {
	var seen *http.Request
	h := RequireSigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	w := httptest.NewRecorder()

	h(w, r)

	if seen == nil {
		t.Fatal("next handler not called")
	}
	if seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("X-User-Sub stripped on valid sig: got %q", seen.Header.Get("X-User-Sub"))
	}
	if seen.Header.Get("X-User-Sig") == "" {
		t.Fatal("X-User-Sig stripped on valid sig")
	}
}

func TestStripUnsigned_NoSub_PassesUntouched(t *testing.T) {
	var seen *http.Request
	h := StripUnsigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil) // public flow
	w := httptest.NewRecorder()

	h(w, r)

	if seen == nil {
		t.Fatal("next handler not called")
	}
	if w.Code != 200 {
		t.Fatalf("status: got %d want 200", w.Code)
	}
}

func TestStripUnsigned_SubWithoutSig_StripsAndContinues(t *testing.T) {
	var seen *http.Request
	h := StripUnsigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sub", "github:hacker")
	r.Header.Set("X-User-Name", "hacker")
	w := httptest.NewRecorder()

	h(w, r)

	if seen == nil {
		t.Fatal("next handler not called")
	}
	for _, h := range identityHeaders {
		if seen.Header.Get(h) != "" {
			t.Fatalf("identity header %q not stripped", h)
		}
	}
}

func TestStripUnsigned_ValidSig_Passes(t *testing.T) {
	var seen *http.Request
	h := StripUnsigned(mwSecret)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	w := httptest.NewRecorder()

	h(w, r)

	if seen == nil {
		t.Fatal("next handler not called")
	}
	if seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("X-User-Sub stripped on valid sig: got %q", seen.Header.Get("X-User-Sub"))
	}
	if seen.Header.Get("X-User-Name") != "alice" {
		t.Fatalf("X-User-Name stripped on valid sig: got %q", seen.Header.Get("X-User-Name"))
	}
}

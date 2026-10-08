package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// transitProof is what the stub authd mints; the endpoint must answer with it
// and never with the caller's own bearer.
const transitProof = "service-proxyd-transit-token"

// forwardAuthSrv is the integ proxy with an ES256 key set, a signer, and a
// service-token source backed by a stub authd — without svc, attachTransit
// DELETES Authorization and sets nothing, so an assertion about the proof
// would pass on code that never attaches one.
func forwardAuthSrv(t *testing.T) (*server, func(sub string) string) {
	t.Helper()
	s, _, _ := newIntegProxy(t, "fwdsecret")
	k, ks := es256KeySet(t)
	s.ks, s.cfg.authdURL = ks, "http://authd"

	authd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + transitProof + `","token_type":"Bearer"}`))
	}))
	t.Cleanup(authd.Close)
	svc, err := auth.ServiceToken(authd.URL, "proxyd", "stub-key")
	if err != nil {
		t.Fatal(err)
	}
	s.svc = svc

	return s, func(sub string) string {
		tok, err := k.Sign(auth.TokenClaims{
			Sub: sub, Typ: "user", Extra: map[string]string{"name": "Fwd User"},
		}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
}

// TestForwardAuth_AnsweredIdentityCarriesTheTransitProof — a backend trusts
// X-User-* only when proxyd's own bearer is on the request. If the 200 answers
// identity WITHOUT it, an off-the-shelf proxy in front would copy headers that
// every backend then treats as untrusted, and the whole surface reads as
// logged-out (6/22 step 3, first constraint).
func TestForwardAuth_AnsweredIdentityCarriesTheTransitProof(t *testing.T) {
	s, sign := forwardAuthSrv(t)

	req := httptest.NewRequest("GET", "/auth/forward?route=/dash/", nil)
	req.Header.Set("X-Forwarded-Uri", "/dash/routd/")
	req.Header.Set("Authorization", "Bearer "+sign("fwd-user"))
	w := httptest.NewRecorder()
	s.handleForwardAuth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-User-Sub"); got != "fwd-user" {
		t.Errorf("X-User-Sub = %q, want fwd-user", got)
	}
	if w.Header().Get("X-User-Groups") == "" {
		t.Error("X-User-Groups absent; the proxy has nothing to copy")
	}
	// The display name is the half of the answer no backend refuses without:
	// webd records it as the sender name on a posted message and dashd shows it
	// in the nav, so an endpoint that omits it anonymises every caller behind a
	// fronting proxy while every request still answers 200.
	if got := w.Header().Get("X-User-Name"); got != "Fwd User" {
		t.Errorf("X-User-Name = %q, want the caller's display name", got)
	}
	// The proof must be PRESENT — a 200 that answers identity without it makes
	// every backend read X-User-* as untrusted, so the whole surface reads as
	// logged out. Asserting only "not the caller's token" passes when the header
	// is absent entirely, which is how this test first missed the mutation.
	got := w.Header().Get("Authorization")
	if got != "Bearer "+transitProof {
		t.Errorf("Authorization = %q, want the minted transit proof", got)
	}
	if strings.Contains(got, sign("fwd-user")) {
		t.Error("the answer echoed the caller's own bearer instead of a transit proof")
	}
}

// TestForwardAuth_RefusalMatchesTheInProcessGate — the endpoint must not be a
// second authentication path that agrees today and diverges later. An
// unauthenticated subrequest gets the same 303-to-login a direct request does.
func TestForwardAuth_RefusalMatchesTheInProcessGate(t *testing.T) {
	s, _ := forwardAuthSrv(t)

	req := httptest.NewRequest("GET", "/auth/forward", nil)
	req.Header.Set("X-Forwarded-Uri", "/dash/routd/")
	w := httptest.NewRecorder()
	s.handleForwardAuth(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 to login", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("Location = %q", loc)
	}
	if w.Header().Get("X-User-Sub") != "" {
		t.Error("a refusal must stamp no identity")
	}
	// The caller's route, not /auth/forward, is what they return to.
	var ret string
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth_return" {
			ret = c.Value
		}
	}
	if ret != "/dash/routd/" {
		t.Errorf("auth_return = %q, want the caller's route", ret)
	}
}

// TestForwardAuth_RefusesWithoutAuthSecret — the same door requireAuth closes.
// AUTH_SECRET keys the OAuth CSRF state, so without it the login flow cannot
// complete and admitting anyone here would be worse than failing.
func TestForwardAuth_RefusesWithoutAuthSecret(t *testing.T) {
	s, sign := forwardAuthSrv(t)
	s.cfg.authSecret = ""

	req := httptest.NewRequest("GET", "/auth/forward", nil)
	req.Header.Set("Authorization", "Bearer "+sign("fwd-user"))
	w := httptest.NewRecorder()
	s.handleForwardAuth(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("an unset AUTH_SECRET must not admit a caller")
	}
	if w.Header().Get("X-User-Sub") != "" {
		t.Error("identity stamped with no auth secret configured")
	}
}

// TestForwardAuth_OptionalRouteAdmitsAnonymously pins BUGS `L405`. `optional` is the
// one route class that exists to serve a caller with no credential — `/chat/<token>/`
// and `/hook/<token>`, where the TOKEN is the credential and a stamped sub only
// decides attribution.
//
// The in-proxyd path has always switched on the class (`main.go` `case "optional"`).
// The forward-auth hop did not: it 303'd anything without a valid credential to
// /auth/login, and `proxycfg/caddy.go` `authenticated` sends `optional` through the
// hop — so the two paths disagreed about the only class meant to be anonymous, and
// every anonymous chat visitor would have landed on a login page under the Caddy
// renderer (6/22 §"Why `optional` attaches the transit proof on both paths").
func TestForwardAuth_OptionalRouteAdmitsAnonymously(t *testing.T) {
	s, _ := forwardAuthSrv(t)
	s.rr = newRoutesResource(nil, []Route{
		{Path: "/chat/", Backend: "http://webd:8080", Auth: "optional"},
		{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
	})

	t.Run("anonymous on an optional route gets the transit proof, not a redirect", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/auth/forward", nil)
		req.Header.Set("X-Forwarded-Uri", "/chat/abc123/")
		w := httptest.NewRecorder()
		s.handleForwardAuth(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("anonymous optional status=%d, want 200 — a login redirect here is the bug", w.Code)
		}
		if got := w.Header().Get("Authorization"); got != "Bearer "+transitProof {
			t.Errorf("Authorization = %q, want the transit proof", got)
		}
		// Anonymous means anonymous: nothing may claim an identity the caller
		// did not present.
		for _, h := range []string{"X-User-Sub", "X-User-Name", "X-User-Groups"} {
			if v := w.Header().Get(h); v != "" {
				t.Errorf("%s = %q on an anonymous request", h, v)
			}
		}
	})

	// The dual, and the half that must not loosen: a `user` route still refuses.
	t.Run("anonymous on a user route still redirects", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/auth/forward", nil)
		req.Header.Set("X-Forwarded-Uri", "/dash/routd/")
		w := httptest.NewRecorder()
		s.handleForwardAuth(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("anonymous user-route status=%d, want 303", w.Code)
		}
	})

	// A path proxyd does not serve cannot talk its way past the gate by claiming
	// to be optional: an unmatched URI takes the stricter answer.
	t.Run("an unmatched path is not optional", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/auth/forward", nil)
		req.Header.Set("X-Forwarded-Uri", "/nope/")
		w := httptest.NewRecorder()
		s.handleForwardAuth(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("unmatched path status=%d, want 303", w.Code)
		}
	})
}

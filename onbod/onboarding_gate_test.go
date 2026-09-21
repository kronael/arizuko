package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/store"
)

// onbod's onboarding surfaces key on the X-User-Sub proxyd stamps, and proxyd
// stamps it only on a route declared `user`: its `public` arm proxies the
// request untouched and its `optional` arm resolves a Bearer, while a browser
// navigation carries a cookie. Declared `public`, every arrival was anonymous,
// so the handler bounced it to /auth/login, OAuth returned at once on a live
// provider session, and the caller landed back on the same route still
// anonymous — a loop the browser ended at its hop limit. Nobody could redeem an
// invite or create a world through the web, and `invite_redemptions` was 0 on
// every instance (BUGS L99).
//
// The declaration lives in `compose`, the handler here, so neither package can
// see the loop alone. These tests read the real declaration and run the real
// handler behind a stand-in for proxyd's gate.

// proxydGate mirrors proxyd's route() auth switch (proxyd/main.go) for a
// browser navigation — a session cookie, no Authorization header. It strips
// client-supplied identity on entry the way stripClientHeaders does, so the
// only X-User-Sub onbod can see is one this gate stamped.
func proxydGate(mode string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-User-Sub")
		switch mode {
		case "public", "optional":
			// Neither arm reads a cookie: `public` proxies untouched, and
			// `optional` only tries a Bearer.
		default:
			// `user` → requireAuth: trade the session for an identity, or bounce
			// to login with the return path.
			c, err := r.Cookie("session")
			if err != nil {
				http.SetCookie(w, &http.Cookie{Name: "auth_return", Value: r.URL.Path, Path: "/"})
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}
			r.Header.Set("X-User-Sub", c.Value)
		}
		next.ServeHTTP(w, r)
	})
}

// onboardingFront stands onbod's real mux behind the gate its routes really
// declare, plus the login authd answers with when the provider session is
// already live: sign the caller in and send them back where they came from.
func onboardingFront(t *testing.T) *httptest.Server {
	t.Helper()
	db := testDB(t)
	onbod := newOnbodMux(db, db, config{}, nil)

	front := http.NewServeMux()
	front.Handle("/onboard", proxydGate(compose.CoreRouteAuth("/onboard"), onbod))
	front.Handle("/invite/", proxydGate(compose.CoreRouteAuth("/invite/"), onbod))
	front.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "github:newcomer", Path: "/"})
		back := "/"
		if c, err := r.Cookie("auth_return"); err == nil && c.Value != "" {
			back = c.Value
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	})

	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)
	return srv
}

// TestOnboardingReachesThePage drives the loop: a browser with an empty jar
// asks for an onboarding page and must arrive at one. A page is reached when
// the walk terminates AND answers 200 — a refusal is not the loop, but it is
// not onboarding either.
func TestOnboardingReachesThePage(t *testing.T) {
	srv := onboardingFront(t)
	for _, path := range []string{"/onboard", "/invite/sometoken"} {
		t.Run(path, func(t *testing.T) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := (&http.Client{Jar: jar}).Get(srv.URL + path)
			if err != nil {
				if strings.Contains(err.Error(), "stopped after") {
					t.Fatalf("%s never stopped redirecting: %v", path, err)
				}
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s answered %d; a signed-in caller must reach the page", path, resp.StatusCode)
			}
		})
	}
}

// TestOnbodRefusesAnonymousWithoutRedirecting pins the loop out of reach by
// construction. proxyd owns the login bounce and the auth_return cookie it
// resumes from (spec 5/31); an onbod that bounced too would loop again the
// moment its transit proof stopped verifying, with the caller already signed
// in. Reaching onbod unidentified is a broken gate, so say so and stop.
func TestOnbodRefusesAnonymousWithoutRedirecting(t *testing.T) {
	db := testDB(t)
	mux := newOnbodMux(db, db, config{}, nil)
	for _, path := range []string{"/onboard", "/invite/sometoken"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d for an unidentified caller, want 401", path, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Errorf("%s redirected an unidentified caller to %q; proxyd owns the login bounce", path, loc)
		}
	}
}

// The advice a stranded row carries has to name the statuses the pipeline
// actually advances, because an operator reads it to repair the row. Asserting
// on the LOG and not on the helper is the point: the stale vocabulary lived at
// the call site, so a test that calls the helper passes either way (BUGS O1 is
// the rows; this is the instruction they carry).
func TestStrandedRowAdviceNamesTheStatusesThatAdvance(t *testing.T) {
	db := ownedDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{
		JID: "telegram:user/stranded", Status: "pending"})

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	warnedStranded.Delete("telegram:user/stranded\x00pending")

	warnStrandedRows(db)

	out := logged.String()
	if !strings.Contains(out, "stranded") {
		t.Fatalf("a row in an unknown status was not reported: %q", out)
	}
	for status := range knownStatuses {
		if !strings.Contains(out, status) {
			t.Errorf("the advice omits %q, which the pipeline does advance: %q", status, out)
		}
	}
	if strings.Contains(out, "token_used") {
		t.Errorf("the advice still names token_used, which nothing advances: %q", out)
	}
}

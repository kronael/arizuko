package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// user_profiles is routd-owned (routd/migrations 0011 + 0025); dashd reads it
// through routdStore(), so routd is the handle to wire.
func profileTestDB(t *testing.T) *dash {
	t.Helper()
	return &dash{routd: routdDB(t)}
}

func seedAuthUser(t *testing.T, d *dash, sub, name string) {
	t.Helper()
	if err := d.routdStore().CreateUserProfile(sub, sub, name); err != nil {
		t.Fatal(err)
	}
}

func TestProfile_NoIdentity(t *testing.T) {
	d := profileTestDB(t)
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/profile/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "no identity") {
		t.Fatalf("expected no identity banner, got %s", w.Body.String())
	}
}

// The signed-in sub's own provider is the only one hidden; every other
// provider offers a link button.
func TestProfile_HidesOwnProviderOffersRest(t *testing.T) {
	d := profileTestDB(t)
	seedAuthUser(t, d, "google:alice", "Alice")

	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/profile/", nil)
	req.Header.Set("X-User-Sub", "google:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "google:alice") {
		t.Fatal("missing canonical sub")
	}
	if strings.Contains(body, `href="/auth/google?intent=link`) {
		t.Fatal("google link button should be hidden")
	}
	for _, p := range []string{"/auth/github?intent=link", "/auth/discord?intent=link"} {
		if !strings.Contains(body, p) {
			t.Fatalf("missing %s button, got: %s", p, body)
		}
	}
}

// The portal tile may promise no more than the page delivers. handleProfile
// reads the caller's own sub and nothing else, so a tile advertising the
// accounts linked to it sends the reader after a list nothing can render
// (BUGS P2).
func TestPortalProfileTilePromisesOnlyWhatProfileShows(t *testing.T) {
	db := routdDB(t)
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "linked accounts") {
		t.Error(`portal tile promises "linked accounts"; /dash/profile/ shows none`)
	}
}

// Exactly one provider is hidden — the caller's own — and an unrecognised
// prefix hides none, so the list is non-empty on every render and the page
// has no all-linked state to write. A filter that could empty it must say
// what the page shows instead (BUGS P2).
func TestProfile_ProviderListIsNeverEmpty(t *testing.T) {
	cases := map[string]int{"unknown:zed": len(supportedProviders)}
	for _, p := range supportedProviders {
		cases[p.prefix+"alice"] = len(supportedProviders) - 1
	}
	for sub, want := range cases {
		if want < 1 {
			t.Fatalf("sub %q wants %d buttons; the arithmetic changed", sub, want)
		}
		d := profileTestDB(t)
		seedAuthUser(t, d, sub, "Alice")
		mux := http.NewServeMux()
		d.registerRoutes(mux)
		req := httptest.NewRequest("GET", "/dash/profile/", nil)
		req.Header.Set("X-User-Sub", sub)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if got := strings.Count(w.Body.String(), "intent=link"); got != want {
			t.Errorf("sub %q: %d link buttons, want %d", sub, got, want)
		}
	}
}

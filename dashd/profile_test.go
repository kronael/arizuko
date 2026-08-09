package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// user_profiles is routd-owned (routd/migrations 0011 + 0025); dashd reads it
// via adminDB() (routd.db), so dbRoutd is the handle to wire.
func profileTestDB(t *testing.T) *dash {
	t.Helper()
	return &dash{dbRoutd: routdDB(t)}
}

func seedAuthUser(t *testing.T, d *dash, sub, name string) {
	t.Helper()
	_, err := d.adminDB().Exec(
		`INSERT INTO user_profiles (sub, username, name, created_at)
		 VALUES (?, ?, ?, '2026-05-01T00:00:00Z')`,
		sub, sub, name)
	if err != nil {
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

// The signed-in sub's own provider is the only one hidden: the profile page no
// longer reads a linked-account list (user_profiles.linked_to_sub is gone —
// nothing ever wrote it), so every other provider offers a link button.
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

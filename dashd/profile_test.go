package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func profileTestDB(t *testing.T) *dash {
	t.Helper()
	db := testDB(t)
	if _, err := db.Exec(
		`CREATE TABLE auth_users (
			id INTEGER PRIMARY KEY,
			sub TEXT UNIQUE NOT NULL,
			username TEXT UNIQUE NOT NULL,
			hash TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			linked_to_sub TEXT
		)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &dash{db: db}
}

func seedAuthUser(t *testing.T, d *dash, sub, name, linked string) {
	t.Helper()
	var ltp interface{}
	if linked != "" {
		ltp = linked
	}
	_, err := d.db.Exec(
		`INSERT INTO auth_users (sub, username, hash, name, created_at, linked_to_sub)
		 VALUES (?, ?, '', ?, '2026-05-01T00:00:00Z', ?)`,
		sub, sub, name, ltp)
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

func TestProfile_ShowsLinkedSubsAndUnlinkedProviders(t *testing.T) {
	d := profileTestDB(t)
	seedAuthUser(t, d, "google:alice", "Alice", "")
	seedAuthUser(t, d, "github:alice2", "Alice GH", "google:alice")

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
	if !strings.Contains(body, "github:alice2") {
		t.Fatal("missing linked sub")
	}
	// Discord link button should be present (alice has google + github, missing discord)
	if !strings.Contains(body, "/auth/discord?intent=link") {
		t.Fatalf("missing discord link button, got: %s", body)
	}
	// google + github already present, so their link buttons must NOT render
	if strings.Contains(body, `href="/auth/google?intent=link`) {
		t.Fatal("google link button should be hidden")
	}
	if strings.Contains(body, `href="/auth/github?intent=link`) {
		t.Fatal("github link button should be hidden")
	}
}

func TestProfile_NoAdditionalLinks(t *testing.T) {
	d := profileTestDB(t)
	seedAuthUser(t, d, "google:alice", "Alice", "")

	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/profile/", nil)
	req.Header.Set("X-User-Sub", "google:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "No additional providers linked") {
		t.Fatalf("expected empty linked list, got: %s", body)
	}
	if !strings.Contains(body, "/auth/github?intent=link") {
		t.Fatalf("expected github link button, got: %s", body)
	}
}

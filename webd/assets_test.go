package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /assets/arizuko-client.js → 200, JS MIME, CORS header, non-empty body.
func TestAssets_ServesSDK(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/arizuko-client.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
	if got := resp.Header.Get("ETag"); got == "" {
		t.Errorf("ETag missing")
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age") {
		t.Errorf("Cache-Control = %q, want max-age", got)
	}
}

// GET /assets/missing.js → 404 (no embedded file).
func TestAssets_404NotEmbedded(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/missing.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// GET /assets/../slink.go → 404, path-traversal rejected before fs lookup.
func TestAssets_RejectsTraversal(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/assets/x", nil)
	req.SetPathValue("path", "../slink.go")
	w := httptest.NewRecorder()
	s.handleAssets(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// OPTIONS /assets/arizuko-client.js → 204 with CORS headers.
func TestAssets_CORS_PreflightOptions(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/assets/arizuko-client.js", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Errorf("ACAM = %q, want includes GET", got)
	}
}

// ETag round-trip — If-None-Match returns 304.
func TestAssets_ETag_NotModified(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/arizuko-client.js")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if etag == "" {
		t.Fatalf("first response missing ETag")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/assets/arizuko-client.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp2.StatusCode)
	}
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestHealthForwardsBackendUp asserts /health returns 200 when the
// backend's /health is reachable.
func TestHealthForwardsBackendUp(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer be.Close()

	srv := httptest.NewServer(healthHandler(be.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
}

// TestHealthForwardsBackendDown asserts /health returns 503 when the
// backend is unreachable.
func TestHealthForwardsBackendDown(t *testing.T) {
	srv := httptest.NewServer(healthHandler("http://127.0.0.1:1")) // nothing listens
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// TestProxyForwardsSpeech asserts /v1/audio/speech proxies through to
// the backend, preserves request body + headers, and pipes the response.
func TestProxyForwardsSpeech(t *testing.T) {
	var gotPath, gotMethod, gotCT string
	var gotBody []byte
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("OggS-fake"))
	}))
	defer be.Close()
	beURL, _ := url.Parse(be.URL)

	srv := httptest.NewServer(proxyHandler(beURL))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"model":"kokoro","voice":"af_bella","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OggS-fake" {
		t.Errorf("body = %q, want OggS-fake", body)
	}
	if gotMethod != "POST" || gotPath != "/v1/audio/speech" || gotCT != "application/json" {
		t.Errorf("backend got %s %s ct=%q (body=%s)", gotMethod, gotPath, gotCT, gotBody)
	}
	if !strings.Contains(string(gotBody), "af_bella") {
		t.Errorf("backend body did not include voice: %s", gotBody)
	}
}

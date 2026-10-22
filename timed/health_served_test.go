package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BUGS L510: /health served through timed's own mux.
func TestTimedHealthServesOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTimedMux(&dashServer{}).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("GET /health = %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

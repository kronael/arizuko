package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BUGS L510: /health served through onbod's own mux.
func TestOnbodHealthServesOK(t *testing.T) {
	db := testDB(t)
	rec := httptest.NewRecorder()
	newOnbodMux(db, db, config{}, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("GET /health = %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore"
)

// BUGS L510: /health was asserted as a registered pattern and never served,
// so a 503 branch rewritten to answer 200 passed every authd test.
func TestAuthdHealthServesOK(t *testing.T) {
	rec := httptest.NewRecorder()
	testMux(t, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("GET /health = %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}

type deadStore struct{}

func (deadStore) Query(context.Context, objstore.Query) ([]objstore.Item, error) {
	return nil, errors.New("connection refused")
}

func (deadStore) GuardedBatch(context.Context, []objstore.Check, []objstore.Write) (bool, error) {
	return false, errors.New("connection refused")
}

func TestAuthdHealthAnswers503WhenTheStoreIsDown(t *testing.T) {
	s := &server{a: &Authd{st: authstore.New(deadStore{})}}
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /health with the store down = %d, want 503", rec.Code)
	}
}

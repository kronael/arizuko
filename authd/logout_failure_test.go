package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore"
)

type failingLogoutStore struct{ objstore.Store }

func (failingLogoutStore) GuardedBatch(context.Context, []objstore.Check, []objstore.Write) (bool, error) {
	return false, errors.New("injected store outage")
}

func TestLogoutReportsRevocationFailure(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	raw, err := a.st.StartRefreshFamily("user:alice", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a.st = authstore.New(failingLogoutStore{db})
	o := &oauth{a: a}
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: "refresh_token", Value: raw})
	w := httptest.NewRecorder()
	o.logout(w, r)
	row, ok := a.st.RefreshTokenBySecret(raw)
	if !ok || row.Revoked {
		t.Fatal("fixture refresh family must remain live")
	}
	if w.Code != http.StatusInternalServerError || w.Header().Get("Set-Cookie") != "" ||
		w.Header().Get("Location") != "" {
		t.Fatalf("logout status = %d, headers = %v", w.Code, w.Header())
	}
}

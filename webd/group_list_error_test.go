package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type groupsFailStore struct{ objstore.Store }

func (s groupsFailStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Groups {
		return nil, errors.New("groups unavailable")
	}
	return s.Store.Query(ctx, q)
}

func TestGroupListsReportReadErrors(t *testing.T) {
	s, _, st := newTestServer(t)
	s.stRoutd = store.NewOver(groupsFailStore{st.Obj()})
	for _, path := range []string{"/api/groups", "/x/groups"} {
		req := httptest.NewRequest("GET", path, nil)
		for k, v := range signUserHeaders(map[string]string{
			"X-User-Sub": "user:alice", "X-User-Groups": `["**"]`,
		}) {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: HTTP=%d body=%q", path, w.Code, w.Body.String())
		}
	}
}

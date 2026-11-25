package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

type routeReads struct {
	objstore.Store
	tables int
	fail   bool
	change func()
}

func (s *routeReads) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.fail {
		return nil, fmt.Errorf("store unavailable")
	}
	rows, err := s.Store.Query(ctx, q)
	if q.Entity == store.ProxydRoutes || q.Entity == store.WebRoutes {
		s.tables++
		if s.change != nil {
			change := s.change
			s.change = nil
			change()
		}
	}
	return rows, err
}

func routeKey(path string) objstore.Key {
	return objstore.Key{Partition: store.OwnerRoutd, Sort: path}
}

// proxyd holds no copy of the route table. A write that moves no version
// marker is what a copy keyed on the marker never sees: the next read must
// answer the row the store holds now.
func TestRoutesAreReadFromTheStoreOnEachCall(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	row := store.ProxydRoute{Path: "/route/", Backend: "http://webd:8080", Auth: "public"}
	if _, _, err := s.SyncProxydRoutes([]store.ProxydRoute{row}); err != nil {
		t.Fatal(err)
	}
	counter := &routeReads{Store: s.Obj()}
	r := newRoutesResource(store.NewOver(counter), nil)
	first, err := r.readRoutes()
	if err != nil || len(first) != 1 || first[0].Backend != row.Backend {
		t.Fatalf("first read: %+v %v", first, err)
	}
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: store.ProxydRoutes, Key: routeKey(row.Path),
		Attrs: map[string]string{"backend": "http://dashd:8080"},
	}}); err != nil || !ok {
		t.Fatalf("rewrite the row under the marker: %v %v", ok, err)
	}
	counter.tables = 0
	second, err := r.readRoutes()
	if err != nil || len(second) != 1 || second[0].Backend != "http://dashd:8080" {
		t.Fatalf("second read answered %+v %v, want the row the store holds now", second, err)
	}
	if counter.tables != 1 {
		t.Errorf("the second read queried the table %d times, want 1", counter.tables)
	}
	counter.fail = true
	if stale, err := r.readRoutes(); err == nil || stale != nil {
		t.Fatal("served routes after a read failure")
	}
}

func TestWebRoutesAreReadFromTheStoreOnEachCall(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	r := newRoutesResource(s, nil)
	if got, err := r.readWebRoutes(); err != nil || len(got) != 0 {
		t.Fatalf("first read: %+v %v", got, err)
	}
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: store.WebRoutes, Key: routeKey("/acme/"),
		Attrs: map[string]string{store.AttrWebFolder: "acme", store.AttrWebAccess: "deny"},
	}}); err != nil || !ok {
		t.Fatalf("write the row under the marker: %v %v", ok, err)
	}
	if got, err := r.readWebRoutes(); err != nil || len(got) != 1 || got[0].Access != "deny" {
		t.Fatalf("second read answered %+v %v, want the deny row the store holds now", got, err)
	}
}

func holdRoutdScope(t *testing.T, s *store.Store) {
	t.Helper()
	scope := store.OwnerScope(store.OwnerRoutd)
	ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: scope.Entity, Key: scope.Key,
		Attrs: map[string]string{"holder": "test", "intent": "test", "since": "now"},
	}})
	if err != nil || !ok {
		t.Fatalf("hold: %v %v", ok, err)
	}
}

// A replace that holds the scope has part-replaced the table, so a read of it
// is refused.
func TestRouteReadsRefuseAHeldReplacement(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	r := newRoutesResource(s, nil)
	if _, err := r.readRoutes(); err != nil {
		t.Fatalf("read before the hold: %v", err)
	}
	holdRoutdScope(t, s)
	if got, err := r.readRoutes(); err == nil || got != nil {
		t.Fatal("served routes during a replace")
	}
	if got, err := r.readWebRoutes(); err == nil || got != nil {
		t.Fatal("served web routes during a replace")
	}
}

func TestRouteReadRetriesAConcurrentMutation(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	row := store.ProxydRoute{Path: "/route/", Backend: "http://old:8080", Auth: "public"}
	if _, _, err := s.SyncProxydRoutes([]store.ProxydRoute{row}); err != nil {
		t.Fatal(err)
	}
	counter := &routeReads{Store: s.Obj()}
	counter.change = func() {
		row.Backend = "http://new:8080"
		if _, _, err := s.SyncProxydRoutes([]store.ProxydRoute{row}); err != nil {
			t.Fatal(err)
		}
	}
	r := newRoutesResource(store.NewOver(counter), nil)
	got, err := r.readRoutes()
	if err != nil || len(got) != 1 || got[0].Backend != row.Backend || counter.tables != 2 {
		t.Fatalf("mixed read: %v, reads=%d", got, counter.tables)
	}
}

func TestUnavailableRouteStateNeverServesPublicFiles(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: store.WebRoutes, Key: routeKey("/pub/private/"), Attrs: map[string]string{store.AttrWebAccess: "deny"}}); err != nil {
		t.Fatal(err)
	}
	counter := &routeReads{Store: s.Obj()}
	rr := newRoutesResource(store.NewOver(counter), nil)
	server := &server{rr: rr, files: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("protected files reached")
		w.WriteHeader(http.StatusOK)
	})}
	request := func(want int) {
		w := httptest.NewRecorder()
		server.route(w, httptest.NewRequest(http.MethodGet, "/pub/private/secret", nil))
		if w.Code != want {
			t.Fatalf("status=%d want=%d", w.Code, want)
		}
	}
	request(http.StatusForbidden)
	counter.fail = true
	request(http.StatusServiceUnavailable)
	counter.fail = false
	holdRoutdScope(t, s)
	request(http.StatusServiceUnavailable)
}

func TestRouteReadsRefuseAMissingVersion(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	scope := store.OwnerScope(store.OwnerRoutd)
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: scope.Entity, Key: scope.Key, Remove: []string{"version"}}}); err != nil || !ok {
		t.Fatalf("remove version: %v %v", ok, err)
	}
	r := newRoutesResource(s, nil)
	if _, err := r.readRoutes(); err == nil {
		t.Fatal("unversioned read accepted")
	}
}

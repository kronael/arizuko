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

type snapshotReads struct {
	objstore.Store
	rows, tables int
	fail         bool
	change       func()
}

func (s *snapshotReads) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.fail {
		return nil, fmt.Errorf("store unavailable")
	}
	rows, err := s.Store.Query(ctx, q)
	s.rows += len(rows)
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

func TestRouteSnapshotsReadOnlyChangedState(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	var rows []store.ProxydRoute
	for i := range 40 {
		rows = append(rows, store.ProxydRoute{Path: fmt.Sprintf("/route/%d/", i), Backend: "http://webd:8080", Auth: "public"})
	}
	if _, _, err := s.SyncProxydRoutes(rows); err != nil {
		t.Fatal(err)
	}
	counter := &snapshotReads{Store: s.Obj()}
	r := newRoutesResource(store.NewOver(counter), nil)
	first, proxies, err := r.snapshot()
	if err != nil || len(first) != 40 || len(proxies) != 40 {
		t.Fatalf("snapshot: %d %d", len(first), len(proxies))
	}
	counter.rows, counter.tables = 0, 0
	second, next, err := r.snapshot()
	if err != nil || len(second) != 40 || counter.rows != 1 || counter.tables != 0 || proxies[first[0].Path] != next[first[0].Path] {
		t.Fatalf("unchanged read: rows=%d tables=%d", counter.rows, counter.tables)
	}
	rows[0].Backend = "http://dashd:8080"
	if _, _, err := s.SyncProxydRoutes(rows); err != nil {
		t.Fatal(err)
	}
	changed, _, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if MatchRoute(changed, rows[0].Path).Backend != rows[0].Backend {
		t.Fatal("new route not visible")
	}
	counter.fail = true
	if stale, _, err := r.snapshot(); err == nil || stale != nil {
		t.Fatal("served stale data after read failure")
	}
}

func TestSnapshotsRejectHeldReplacement(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	r := newRoutesResource(s, nil)
	if got, _, err := r.snapshot(); err != nil || got == nil {
		t.Fatal("initial snapshot failed")
	}
	_, _ = r.webSnapshot()
	scope := store.OwnerScope(store.OwnerRoutd)
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: scope.Entity, Key: scope.Key, Attrs: map[string]string{"holder": "test", "intent": "test", "since": "now"}}}); err != nil || !ok {
		t.Fatalf("hold: %v %v", ok, err)
	}
	if got, _, err := r.snapshot(); err == nil || got != nil {
		t.Fatal("served route snapshot during replace")
	}
	if got, err := r.webSnapshot(); err == nil || got != nil {
		t.Fatal("served web snapshot during replace")
	}
}

func TestSnapshotRetriesConcurrentMutation(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	row := store.ProxydRoute{Path: "/route/", Backend: "http://old:8080", Auth: "public"}
	if _, _, err := s.SyncProxydRoutes([]store.ProxydRoute{row}); err != nil {
		t.Fatal(err)
	}
	counter := &snapshotReads{Store: s.Obj()}
	counter.change = func() {
		row.Backend = "http://new:8080"
		if _, _, err := s.SyncProxydRoutes([]store.ProxydRoute{row}); err != nil {
			t.Fatal(err)
		}
	}
	r := newRoutesResource(store.NewOver(counter), nil)
	got, proxies, err := r.snapshot()
	if err != nil || len(got) != 1 || got[0].Backend != row.Backend || len(proxies) != 1 || counter.tables != 2 {
		t.Fatalf("mixed snapshot: %v, reads=%d", got, counter.tables)
	}
}

func TestWebSnapshotReadsOnlyChangedState(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	counter := &snapshotReads{Store: s.Obj()}
	r := newRoutesResource(store.NewOver(counter), nil)
	_, _ = r.webSnapshot()
	counter.rows, counter.tables = 0, 0
	_, _ = r.webSnapshot()
	if counter.rows != 1 || counter.tables != 0 {
		t.Fatalf("unchanged web: rows=%d tables=%d", counter.rows, counter.tables)
	}
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: store.WebRoutes, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "/acme/"}, Attrs: map[string]string{store.AttrWebFolder: "acme", store.AttrWebAccess: "public"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.webSnapshot(); err != nil || len(got) != 1 || got[0].Access != "public" {
		t.Fatalf("web mutation absent: %v", got)
	}
}

func TestUnavailableSnapshotsNeverServePublicFiles(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: store.WebRoutes, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "/pub/private/"}, Attrs: map[string]string{store.AttrWebAccess: "deny"}}); err != nil {
		t.Fatal(err)
	}
	counter := &snapshotReads{Store: s.Obj()}
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
	scope := store.OwnerScope(store.OwnerRoutd)
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: scope.Entity, Key: scope.Key, Attrs: map[string]string{"holder": "test", "intent": "test", "since": "now"}}}); err != nil || !ok {
		t.Fatalf("hold: %v %v", ok, err)
	}
	request(http.StatusServiceUnavailable)
}

func TestSnapshotsRejectMissingVersion(t *testing.T) {
	s := testutils.NewRoutdStore(t)
	scope := store.OwnerScope(store.OwnerRoutd)
	if ok, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: scope.Entity, Key: scope.Key, Remove: []string{"version"}}}); err != nil || !ok {
		t.Fatalf("remove version: %v %v", ok, err)
	}
	r := newRoutesResource(s, nil)
	if _, _, err := r.snapshot(); err == nil {
		t.Fatal("unversioned snapshot accepted")
	}
}

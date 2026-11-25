package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// BUGS P3: route matching read `proxyd_routes` and proxy selection read it
// AGAIN, so one request could be matched against one version of the table and
// forwarded against another — a route's auth policy paired with a different
// backend, or a false 404 when the matched path was gone by the second read.
//
// A dispatch forwards to the backend the matched route itself names: the table
// can change or lose the row after the match and the request still reaches
// the backend it was matched to.
func TestDispatchForwardsToTheBackendOfTheMatchedRoute(t *testing.T) {
	var hits atomic.Int32
	matched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer matched.Close()
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the request reached the backend the table was changed to")
	}))
	defer other.Close()

	st := testutils.NewRoutdStore(t)
	route := store.ProxydRoute{Path: "/panel/", Backend: matched.URL, Auth: "public"}
	if _, _, err := st.SyncProxydRoutes([]store.ProxydRoute{route}); err != nil {
		t.Fatal(err)
	}
	s := &server{rr: newRoutesResource(st, nil)}
	routes, err := s.routes()
	if err != nil {
		t.Fatal(err)
	}
	rt := MatchRoute(routes, "/panel/hello")
	if rt == nil {
		t.Fatal("fixture: /panel/ does not match")
	}
	route.Backend = other.URL
	if _, _, err := st.SyncProxydRoutes([]store.ProxydRoute{route}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.dispatchRoute(rt, rec, httptest.NewRequest(http.MethodGet, "/panel/hello", nil))
	if rec.Code != http.StatusOK || hits.Load() != 1 {
		t.Errorf("dispatch answered %d after %d hits on the matched backend", rec.Code, hits.Load())
	}
}

// THE MATCHED ROUTE itself changes under live traffic. A request may
// legitimately miss (the route really was gone when it read the table) or
// legitimately succeed — but it must never match a route and then fail to
// reach that route's backend.
func TestConcurrentRouteMutationNeverSplitsADispatch(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	st := testutils.NewRoutdStore(t)
	route := store.ProxydRoute{Path: "/panel/", Backend: up.URL, Auth: "public"}
	if _, _, err := st.SyncProxydRoutes([]store.ProxydRoute{route}); err != nil {
		t.Fatal(err)
	}

	s := &server{rr: newRoutesResource(st, nil)}

	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Add(1)
	go func() {
		defer churn.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			churnRoute(st, route, i%2 != 0)
		}
	}()

	var split, matched, missed atomic.Int32
	var reqs sync.WaitGroup
	for range 8 {
		reqs.Add(1)
		go func() {
			defer reqs.Done()
			for range 60 {
				routes, err := s.routes()
				if err != nil {
					continue
				}
				rt := MatchRoute(routes, "/panel/hello")
				if rt == nil {
					// Legitimate: the route was genuinely absent in this read.
					missed.Add(1)
					continue
				}
				matched.Add(1)
				rec := httptest.NewRecorder()
				s.dispatchRoute(rt, rec, httptest.NewRequest(http.MethodGet, "/panel/hello", nil))
				if rec.Code != http.StatusOK {
					split.Add(1)
				}
			}
		}()
	}
	reqs.Wait()
	close(stop)
	churn.Wait()

	// Both outcomes must actually occur, or the race never happened and a pass
	// means nothing.
	if matched.Load() == 0 || missed.Load() == 0 {
		t.Fatalf("fixture is vacuous: matched=%d missed=%d, want both non-zero",
			matched.Load(), missed.Load())
	}
	if split.Load() != 0 {
		t.Errorf("%d of %d matched requests did not reach the matched backend",
			split.Load(), matched.Load())
	}
}

// churnRoute adds or removes one route through the writes /v1/proxyd_routes
// itself runs, each as its own committed batch. One batch that deleted and
// re-inserted would never let a concurrent reader see the row absent, and the
// absent read is half of what this race needs.
func churnRoute(st *store.Store, r store.ProxydRoute, add bool) {
	if add {
		_ = insertProxydRoute(context.Background(), st, r)
		return
	}
	_, _ = st.DeleteProxydRoute(context.Background(), r.Path)
}

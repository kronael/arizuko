package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/store"
)

// BUGS P3: route matching read `proxyd_routes` and proxy selection read it
// AGAIN, so one request could be matched against one version of the table and
// forwarded against another — a route's auth policy paired with a different
// backend, or a false 404 when the matched path was gone by the second read.
//
// The invariant the fix establishes: a snapshot always carries a proxy for
// every non-redirect route it lists, so a matched route can never be missing
// its backend.
func TestSnapshotCarriesAProxyForEveryRoute(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, r := range []store.ProxydRoute{
		{Path: "/chat/", Backend: "http://webd:8080", Auth: "public"},
		{Path: "/panel/", Backend: "http://dashd:8080", Auth: "user"},
		{Path: "/dav/", Backend: "http://davd:8080", Auth: "user", StripPrefix: true},
		{Path: "/old/", RedirectTo: "/panel/"},
	} {
		if err := st.PutProxydRoute(r); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{rr: newRoutesResource(st, nil)}
	routes, proxies := s.routeSnapshot()
	if len(routes) != 4 {
		t.Fatalf("fixture: %d routes, want 4", len(routes))
	}
	for _, rt := range routes {
		_, ok := proxies[rt.Path]
		if rt.RedirectTo != "" {
			if ok {
				t.Errorf("redirect route %s got a proxy", rt.Path)
			}
			continue
		}
		if !ok {
			t.Errorf("route %s matched in a snapshot that has no proxy for it", rt.Path)
		}
	}
}

// The mutation this fix is about: THE MATCHED ROUTE itself changing under live
// traffic. A request may legitimately miss (the route really was gone when it
// read the table) or legitimately succeed — but it must never match a route and
// then fail to find that route's backend. That combination is only reachable
// when matching and proxy selection read the table separately, and it is what
// used to answer a bare 404 (now a loud 500, since it can no longer happen).
func TestConcurrentRouteMutationNeverSplitsAcrossSnapshots(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	route := store.ProxydRoute{Path: "/panel/", Backend: up.URL, Auth: "public"}
	if err := st.PutProxydRoute(route); err != nil {
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
			if i%2 == 0 {
				_, _ = st.DeleteProxydRoute(route.Path)
			} else {
				_ = st.PutProxydRoute(route)
			}
		}
	}()

	var split, matched, missed atomic.Int32
	var reqs sync.WaitGroup
	for range 8 {
		reqs.Add(1)
		go func() {
			defer reqs.Done()
			for range 60 {
				routes, proxies := s.routeSnapshot()
				rt := MatchRoute(routes, "/panel/hello")
				if rt == nil {
					// Legitimate: the route was genuinely absent in this read.
					missed.Add(1)
					continue
				}
				matched.Add(1)
				rec := httptest.NewRecorder()
				s.dispatchRoute(rt, proxies, rec, httptest.NewRequest(http.MethodGet, "/panel/hello", nil))
				if rec.Code == http.StatusInternalServerError {
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
		t.Errorf("%d of %d matched requests found no backend — dispatch split across two snapshots",
			split.Load(), matched.Load())
	}
}

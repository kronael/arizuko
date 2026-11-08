package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type routeSlip struct {
	objstore.Store
	entity    string
	count     int
	at        func()
	seenRoute bool
}

func (s *routeSlip) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := s.Store.Query(ctx, q)
	if q.Entity == store.Routes {
		s.seenRoute = true
	}
	if err == nil && q.Entity == s.entity && (s.entity != store.ScopeEntity || s.seenRoute) {
		s.count--
		if s.count == 0 {
			s.at()
		}
	}
	return items, err
}

func TestRouteMutationRejectsReassignment(t *testing.T) {
	for _, method := range []string{"update", "delete"} {
		for _, point := range []string{"snapshot", "writer", "same-coordinate"} {
			t.Run(method+"/"+point, func(t *testing.T) {
				d, raw := splitAdminDash(t, "alice@x")
				seedFolderAdmin(t, d, "mallory@x", "mallory")
				id := seedRoute(t, raw, core.Route{Seq: 1, Match: "platform=slack", Target: "mallory"})
				operator := core.Route{ID: id, Seq: 2, Match: "platform=github", Target: "team"}
				if point == "same-coordinate" {
					operator = core.Route{ID: id, Seq: 1, Match: "platform=slack", Target: "mallory", ObserveWindowChars: 100}
				}
				fired := false
				var mutationErr error
				entity := store.ScopeEntity
				if point != "snapshot" {
					entity = store.Routes
				}
				count := 2
				if point == "snapshot" {
					count = 1
				}
				d.routd = &routeSlip{Store: raw, entity: entity, count: count, at: func() {
					fired = true
					_, mutationErr = store.NewOver(raw).UpdateRoute(id, operator, "")
				}}
				path := "/dash/routes/" + strconv.FormatInt(id, 10)
				form := url.Values{"seq": {"1"}, "match": {"platform=slack"}, "target": {"mallory"}}.Encode()
				verb := "PATCH"
				action := "route.update"
				if method == "delete" {
					verb, path, form, action = "POST", path+"/delete", "", "route.delete"
				}
				w := httptest.NewRecorder()
				newMux(d).ServeHTTP(w, adminReq(verb, path, form, "mallory@x"))
				got, found, err := store.NewOver(raw).RouteByID(id)
				if !fired || mutationErr != nil || err != nil || !found || !reflect.DeepEqual(got, operator) || w.Code != http.StatusConflict {
					t.Fatalf("fired=%v mutation=%v status=%d route=%+v found=%v err=%v", fired, mutationErr, w.Code, got, found, err)
				}
				all, err := store.NewOver(raw).RouteTable()
				if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], operator) {
					t.Fatalf("routes=%+v err=%v", all, err)
				}
				for _, row := range auditRowsFor(t, raw, action) {
					if row.ActorSub == "mallory@x" {
						t.Fatalf("stale mutation audited: %+v", row)
					}
				}
			})
		}
	}
}

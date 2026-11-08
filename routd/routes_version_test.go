package routd

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type routeVersionSlip struct {
	objstore.Store
	entity    string
	count     int
	at        func()
	seenRoute bool
}

func (s *routeVersionSlip) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
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

func TestRoutesRejectConcurrentReassignment(t *testing.T) {
	for _, surface := range []string{"rest", "rest-open", "mcp"} {
		for _, action := range []string{"delete", "set"} {
			points := []string{"snapshot", "writer"}
			if action == "delete" {
				points = append(points, "containment")
			}
			for _, point := range points {
				t.Run(surface+"/"+action+"/"+point, func(t *testing.T) {
					db, err := OpenMem()
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { db.Close() })
					if err := db.PutGroup(core.Group{Folder: "alice"}); err != nil {
						t.Fatal(err)
					}
					id, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "alice"})
					if err != nil {
						t.Fatal(err)
					}
					grantMCPTools(t, db, "alice", routeToolNames...)
					raw := db.obj
					operator := core.Route{ID: id, Seq: 2, Match: "platform=github", Target: "bob"}
					if action == "set" || point == "snapshot" {
						operator.Target = "alice"
					}
					fired := false
					var mutationErr error
					entity := store.ScopeEntity
					count := 1
					if point == "writer" {
						entity, count = store.Routes, 3
					}
					if point == "containment" {
						entity, count = store.Routes, 2
					}
					db.obj = &routeVersionSlip{Store: raw, entity: entity, count: count, at: func() {
						fired = true
						_, mutationErr = store.NewOver(raw).UpdateRoute(id, operator, "")
					}}
					failure := false
					if surface != "mcp" {
						var verifier Verifier
						if surface == "rest" {
							verifier = fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "alice"}
						}
						srv := NewServer(db, nil, &recDeliverer{}, verifier, 0, "")
						var status int
						if action == "delete" {
							status = doJSON(t, srv.Handler(), "DELETE", "/v1/routes/"+strconv.FormatInt(id, 10), "", nil).Code
						} else {
							status = doJSON(t, srv.Handler(), "PUT", "/v1/routes", "", map[string]any{"routes": []core.Route{}}).Code
						}
						failure = status == http.StatusConflict
					} else {
						sock := serveRoutesMCP(t, db, "alice", "folder:alice")
						var result string
						if action == "delete" {
							_, result = callToolOverSock(t, sock, "delete_route", map[string]any{"id": float64(id)})
						} else {
							_, result = callToolOverSock(t, sock, "set_routes", map[string]any{"routes": "[]"})
						}
						failure = strings.Contains(result, objstore.ErrScopeMoved.Error())
					}
					got, found, err := store.NewOver(raw).RouteByID(id)
					if !fired || mutationErr != nil || err != nil || !found || !reflect.DeepEqual(got, operator) || !failure {
						t.Fatalf("fired=%v mutation=%v failure=%v route=%+v found=%v err=%v", fired, mutationErr, failure, got, found, err)
					}
					all, err := store.NewOver(raw).RouteTable()
					if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], operator) {
						t.Fatalf("routes=%+v err=%v", all, err)
					}
					rows, err := audit.Query(context.Background(), raw, audit.Filter{Limit: audit.MaxLimit})
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						actor := "user:u"
						if surface == "mcp" {
							actor = "folder:alice"
						}
						auditSurface := surface
						if surface == "rest-open" {
							actor, auditSurface = "", "rest"
						}
						if row.Surface == auditSurface && row.ActorSub == actor && row.Action == "routes:"+action && row.Outcome == audit.OutcomeOK {
							t.Fatalf("stale mutation audited: %+v", row)
						}
					}
				})
			}
		}
	}
}

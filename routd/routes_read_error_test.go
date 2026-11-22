package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

type routeReadFailure struct{ objstore.Store }

func (s routeReadFailure) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Routes {
		return nil, errors.New("route read failed")
	}
	return s.Store.Query(ctx, q)
}
func TestRouteListReadErrorBothSurfaces(t *testing.T) {
	db := memDB(t)
	db.obj = routeReadFailure{db.obj}
	s := NewServer(db, nil, nil, nil, 0, "")
	for _, surface := range []string{audit.SurfaceMCP, audit.SurfaceREST} {
		_, err := s.routesHandler(context.Background(), resreg.Execution{Action: resreg.ActionList, Surface: surface}, nil)
		if err == nil {
			t.Errorf("%s: route read failure was hidden", surface)
		}
	}
}

type routeGuardReadFailure struct {
	objstore.Store
	reads int
}

func (s *routeGuardReadFailure) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Routes {
		s.reads++
		if s.reads == 2 {
			return nil, errors.New("route guard read failed")
		}
	}
	return s.Store.Query(ctx, q)
}

func TestRouteSetGuardReadError(t *testing.T) {
	db := memDB(t)
	if _, err := db.AddRoute(core.Route{Seq: 0, Match: "platform=telegram", Target: "alice"}); err != nil {
		t.Fatal(err)
	}
	db.obj = &routeGuardReadFailure{Store: db.obj}
	v := fakeVerifier{sub: "alice", folder: "alice", scope: []string{"routes:write"}}
	h := NewServer(db, nil, nil, v, 0, "").Handler()
	r := doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []core.Route{}})
	if r.Code != 500 {
		t.Errorf("status = %d, want 500: %s", r.Code, r.Body.String())
	}
	routes, err := db.Routes()
	if err != nil || len(routes) != 1 {
		t.Errorf("routes = %v, err = %v; want protected route", routes, err)
	}
}

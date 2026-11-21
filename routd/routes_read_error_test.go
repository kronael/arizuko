package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/audit"
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

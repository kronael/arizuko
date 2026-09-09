package store

// web_routes.go holds `web_routes`: one item per web access rule, addressed by
// its path prefix.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `web_routes` is a registered resreg resource, and an entity-backed resource's
// items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd). A second spelling would put a writer's items in one
// partition and the engine's read in another, and the failure is silent: a query
// on a partition nobody wrote to is a legal query that selects nothing.
//
// THE PATH PREFIX IS THE COORDINATE, so the whole read comes back in
// `ORDER BY path_prefix` with no sort of its own: proxyd snapshots the
// inventory and longest-prefix-matches it in Go (`proxyd/main.go:159`).
//
// THE COORDINATE AND THE ATTRIBUTE NAMES ARE DECLARED HERE AND WRITTEN BY routd.
// This package serves the one read proxyd needs; routd owns every write
// (`web_routes_resource.go`, on resreg's transaction). That is one declaration
// with two readers at different TIMES rather than two formats — the names are
// EXPORTED for exactly that reason, and the exported half is the marker that the
// writer lives in another package. There is deliberately no writer and no
// matcher here: a second writer is how `created_at` came to mean two different
// things.
//
// `created_at` IS core.TS, at 30 bytes. That is the width resreg's own entity
// path stamps a StampedField at (resreg/entity.go itemFor), and every writer of
// these items has to agree byte for byte — a comparison over TEXT answers by
// byte order (BUGS L97).

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a web route item carries. `path_prefix` is NOT among them: it
// is the coordinate, and storing it twice is two facts that can disagree.
const (
	AttrWebAccess     = "access"
	AttrWebRedirectTo = "redirect_to"
	AttrWebFolder     = "folder"
	AttrWebCreatedAt  = "created_at"
)

type WebRoute struct {
	PathPrefix string
	Access     string // public | auth | deny | redirect
	RedirectTo string
	Folder     string
	CreatedAt  time.Time
}

// WebRouteKey is the coordinate of one rule.
func WebRouteKey(pathPrefix string) (objstore.Key, error) {
	if pathPrefix == "" {
		return objstore.Key{}, fmt.Errorf("web route carries no path prefix, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: pathPrefix}, nil
}

// WebRouteFrom reads one item back. An absent `redirect_to` is the empty string,
// which is the only null this model has and exactly what `COALESCE(…,”)`
// answered.
func WebRouteFrom(it objstore.Item) WebRoute {
	wr := WebRoute{
		PathPrefix: it.Key.Sort,
		Access:     it.Attrs[AttrWebAccess],
		RedirectTo: it.Attrs[AttrWebRedirectTo],
		Folder:     it.Attrs[AttrWebFolder],
	}
	wr.CreatedAt, _ = time.Parse(core.TSLayout, it.Attrs[AttrWebCreatedAt])
	return wr
}

// AllWebRoutes returns every web_route, ordered by prefix. proxyd's snapshot.
func (s *Store) AllWebRoutes() ([]WebRoute, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: WebRoutes, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read web routes: %w", err)
	}
	out := make([]WebRoute, 0, len(items))
	for _, it := range items {
		out = append(out, WebRouteFrom(it))
	}
	return out, nil
}

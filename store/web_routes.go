package store

// web_routes.go holds `web_routes`: one item per web access rule, addressed by
// its path prefix.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. The path prefix is the coordinate, so the read comes back
// in `ORDER BY path_prefix`; proxyd snapshots it and longest-prefix-matches in
// Go.
//
// The coordinate and the attribute names are declared here and WRITTEN BY
// routd (`web_routes_resource.go`): one declaration with two readers, which is
// why the names are EXPORTED. There is deliberately no writer and no matcher
// here: a second writer is how `created_at` came to mean two different things.
// `created_at` is core.TS at 30 bytes, the width resreg's entity path stamps
// (BUGS L97).

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a web route item carries. `path_prefix` is the coordinate.
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

// WebRouteFrom reads one item back. An absent `redirect_to` is the empty
// string.
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

// AllWebRoutes returns every web_route, ordered by prefix: proxyd's snapshot.
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

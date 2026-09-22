package store

// proxyd_routes.go holds every read and write of `proxyd_routes`: one item per
// reverse-proxy route, addressed by its path.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. The path is the coordinate, so the whole read comes back
// in `ORDER BY path`; proxyd snapshots it and longest-prefix-matches in Go.
//
// `preserve_headers` is a []string written as JSON text and `strip_prefix` a
// bool written as "0" or "1": the encodings the resource's `db:` tags declare
// (resreg/codec.go), because both writers must agree byte for byte. Both are
// ALWAYS written, empty included: a Go slice and a Go bool have no unset. A
// nil slice renders `[]`, never `null`.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a route item carries. `path` is the coordinate.
const (
	attrPxBackend    = "backend"
	attrPxAuth       = "auth"
	attrPxGatedBy    = "gated_by"
	attrPxHeaders    = "preserve_headers"
	attrPxStrip      = "strip_prefix"
	attrPxRedirectTo = "redirect_to"
	attrPxOwner      = "owner"
)

// proxydRouteKey is the coordinate of one route.
func proxydRouteKey(path string) (objstore.Key, error) {
	if path == "" {
		return objstore.Key{}, fmt.Errorf("proxyd route carries no path, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: path}, nil
}

// proxydRouteFrom reads one item back. A `preserve_headers` that is not the
// JSON document it claims to be is a LOUD fault: a route read wrong is a
// header stripped from every request it proxies.
func proxydRouteFrom(it objstore.Item) (ProxydRoute, error) {
	r := ProxydRoute{
		Path:        it.Key.Sort,
		Backend:     it.Attrs[attrPxBackend],
		Auth:        it.Attrs[attrPxAuth],
		GatedBy:     it.Attrs[attrPxGatedBy],
		RedirectTo:  it.Attrs[attrPxRedirectTo],
		Owner:       it.Attrs[attrPxOwner],
		StripPrefix: it.Attrs[attrPxStrip] == "1",
	}
	if raw := it.Attrs[attrPxHeaders]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &r.PreserveHeaders); err != nil {
			return ProxydRoute{}, fmt.Errorf("decode preserve_headers for %q: %w", r.Path, err)
		}
	}
	return r, nil
}

// proxydRouteAttrs renders the attributes one route carries.
func proxydRouteAttrs(r ProxydRoute) (map[string]string, error) {
	headers := r.PreserveHeaders
	if headers == nil {
		headers = []string{}
	}
	doc, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("encode preserve_headers for %q: %w", r.Path, err)
	}
	attrs := map[string]string{
		attrPxHeaders: string(doc),
		attrPxStrip:   "0",
	}
	if r.StripPrefix {
		attrs[attrPxStrip] = "1"
	}
	setAttr(attrs, attrPxBackend, r.Backend)
	setAttr(attrs, attrPxAuth, r.Auth)
	setAttr(attrs, attrPxGatedBy, r.GatedBy)
	setAttr(attrs, attrPxRedirectTo, r.RedirectTo)
	setAttr(attrs, attrPxOwner, r.Owner)
	return attrs, nil
}

// allProxydRouteItems is the whole table, in coordinate order.
func (s *Store) allProxydRouteItems(ctx context.Context) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: ProxydRoutes, Partition: OwnerRoutd})
	if err != nil {
		return nil, fmt.Errorf("read proxyd routes: %w", err)
	}
	return items, nil
}

// Route ownership (BUGS L45, L46). `owner` records which of three authorities
// wrote the row: RouteOwnerArizuko (`arizuko generate`, rewritten on every
// run), `package:<name>` (its own manifest, rewritten too), or
// RouteOwnerOperator (declared through /v1/proxyd_routes; generate never
// touches it). Editing an arizuko- or package-owned row is not an override;
// taking a path over is DELETE then POST.
const (
	RouteOwnerArizuko       = "arizuko"
	RouteOwnerOperator      = "operator"
	RouteOwnerPackagePrefix = "package:"
)

// RouteOwnerPackage is the owner value for a row an installed package declared.
// A package may never write `arizuko` or `operator` itself (BUGS L46): the
// first would pass as core-shipped, the second would opt out of every later
// generate.
func RouteOwnerPackage(name string) string { return RouteOwnerPackagePrefix + name }

// RouteSurvivesGenerate reports whether a row with this owner is left alone by
// SyncProxydRoutes. Exactly one owner is: `operator`. One predicate, because
// two drifted: the keep loop allowlisted `operator` while the PATCH guard
// denylisted `arizuko`, so a package row was silently overwritten at the next
// deploy (BUGS L45, L81).
func RouteSurvivesGenerate(owner string) bool { return owner == RouteOwnerOperator }

// ProxydRoute mirrors proxyd's `Route` shape for persistence, without importing
// proxyd so the store stays a leaf.
type ProxydRoute struct {
	Path            string   `json:"path"`
	Backend         string   `json:"backend"`
	Auth            string   `json:"auth"`
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
	RedirectTo      string   `json:"redirect_to,omitempty"`
	Owner           string   `json:"owner,omitempty"`
}

// AllProxydRoutes is proxyd's snapshot of the whole table, in path order.
func (s *Store) AllProxydRoutes() ([]ProxydRoute, error) {
	items, err := s.allProxydRouteItems(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ProxydRoute, 0, len(items))
	for _, it := range items {
		r, err := proxydRouteFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// CreateProxydRoute writes ONE route and reports whether it landed, for
// proxyd's dashboard form. created=false is the PATH ALREADY BEING TAKEN, an
// answer rather than an overwrite: an unconditional Put would silently hand an
// arizuko- or package-owned route to the operator and exempt it from every
// later generate (BUGS L45, L46).
func (s *Store) CreateProxydRoute(ctx context.Context, r ProxydRoute) (bool, error) {
	w, err := proxydRouteCreateWrite(r)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(ctx, nil, w)
}

// CreateProxydRouteInto buffers the same create on the caller's write set, for
// proxyd's `/v1/proxyd_routes` POST. refused is what a taken path means.
func (s *Store) CreateProxydRouteInto(ws *WriteSet, r ProxydRoute, refused error) error {
	w, err := proxydRouteCreateWrite(r)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, w)
}

// proxydRouteCreateWrite renders the create both doors above issue.
func proxydRouteCreateWrite(r ProxydRoute) (objstore.Write, error) {
	key, err := proxydRouteKey(r.Path)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs, err := proxydRouteAttrs(r)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Put, Entity: ProxydRoutes, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, nil
}

// UpdateProxydRouteInto buffers the rewrite of one route's mutable half on the
// caller's write set; refused is what "no route at that path" means. `owner`
// is NOT among the fields it writes: ownership is stamped by whoever created
// the row, which is why this is an Update and not a Put. A field the caller
// clears is REMOVED rather than set to "": an empty `gated_by` still answers a
// presence test, and the gate would keep applying to a route the operator
// opened.
func (s *Store) UpdateProxydRouteInto(ws *WriteSet, r ProxydRoute, refused error) error {
	key, err := proxydRouteKey(r.Path)
	if err != nil {
		return err
	}
	attrs, err := proxydRouteAttrs(r)
	if err != nil {
		return err
	}
	delete(attrs, attrPxOwner)
	var remove []string
	for _, name := range []string{attrPxBackend, attrPxAuth, attrPxGatedBy, attrPxRedirectTo} {
		if _, set := attrs[name]; !set {
			remove = append(remove, name)
		}
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Update, Entity: ProxydRoutes, Key: key, Attrs: attrs, Remove: remove,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// DeleteProxydRoute removes one route and reports whether one went, for
// proxyd's dashboard form: the operator is told "no route at that path" rather
// than redirected as if the route were gone.
func (s *Store) DeleteProxydRoute(ctx context.Context, path string) (bool, error) {
	w, err := proxydRouteDeleteWrite(path)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(ctx, nil, w)
}

// DeleteProxydRouteInto buffers the removal on the caller's write set, for
// proxyd's `/v1` face. It carries NO condition: `/v1` declares DELETE
// idempotent (a second delete answers 204), and a write that refused would
// take the audit row beside it down with nothing said.
func (s *Store) DeleteProxydRouteInto(ws *WriteSet, path string) error {
	key, err := proxydRouteKey(path)
	if err != nil {
		return err
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: ProxydRoutes, Key: key})
	return nil
}

// proxydRouteDeleteWrite renders the dashboard's delete, conditioned on the
// route being there so its answer can be reported.
func proxydRouteDeleteWrite(path string) (objstore.Write, error) {
	key, err := proxydRouteKey(path)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Delete, Entity: ProxydRoutes, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}, nil
}

// SyncProxydRoutes writes the release's route table into proxyd_routes and
// reports how many rows it wrote and how many paths it left to the operator.
// proxyd reads the table live, so a route added to a release takes effect on
// the next generate with no restart (BUGS L45).
//
// Two rules: a path whose row is RouteOwnerOperator is SKIPPED whatever it
// points at, and every other shipped path is overwritten keeping the owner the
// caller stamped. A package route is re-derived from its manifest on every
// generate, so leaving it alone would freeze an upgraded package at its first
// backend (BUGS L46). It never deletes, and it is audited once for the whole
// set.
func (s *Store) SyncProxydRoutes(routes []ProxydRoute) (written, kept int, err error) {
	ctx := context.Background()
	actor, actorSub, surface := s.auditIdentity()
	err = func() error {
		owners, err := s.proxydRouteOwners(ctx)
		if err != nil {
			return err
		}
		// Chunked: each path is its own item and the operator-owned ones are
		// left alone either way, so a part-way failure leaves the rest exactly
		// as generate found them.
		written, kept = 0, 0
		var writes []objstore.Write
		for _, r := range routes {
			if RouteSurvivesGenerate(owners[r.Path]) {
				kept++
				continue
			}
			if r.Owner == "" {
				r.Owner = RouteOwnerArizuko
			}
			key, err := proxydRouteKey(r.Path)
			if err != nil {
				return err
			}
			attrs, err := proxydRouteAttrs(r)
			if err != nil {
				return err
			}
			writes = append(writes, objstore.Write{
				Op: objstore.Put, Entity: ProxydRoutes, Key: key, Attrs: attrs,
			})
			written++
		}
		if _, err := applyChunks(ctx, "sync proxyd routes", writes,
			func(ctx context.Context, chunk []objstore.Write) (bool, error) {
				return s.TryWriteEntities(ctx, nil, chunk...)
			}); err != nil {
			return err
		}
		_, rec := audit.WriteFor(audit.Event{
			Category: audit.CategoryMutation,
			Action:   "proxyd_route.sync",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "proxyd_routes",
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"written":       written,
				"kept_operator": kept,
			},
		})
		return s.WriteEntities(ctx, nil, rec)
	}()
	return written, kept, err
}

// proxydRouteOwners reads path→owner for every existing route: which paths the
// operator has taken over is decided BEFORE the first write of the sweep.
func (s *Store) proxydRouteOwners(ctx context.Context) (map[string]string, error) {
	items, err := s.allProxydRouteItems(ctx)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(items))
	for _, it := range items {
		owners[it.Key.Sort] = it.Attrs[attrPxOwner]
	}
	return owners, nil
}

package store

// proxyd_routes.go holds every read and write of `proxyd_routes`: one item per
// reverse-proxy route, addressed by its path.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `proxyd_routes` is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd). A second spelling would put this file's items in one
// partition and the engine's read in another, and the failure is silent: a query
// on a partition nobody wrote to is a legal query that selects nothing.
//
// THE PATH IS THE COORDINATE, which is what makes the whole read come back in
// `ORDER BY path` with no sort of its own: proxyd snapshots the table once and
// longest-prefix-matches in Go, so the order it arrives in is the order it
// serves.
//
// TWO ATTRIBUTES ARE NOT TEXT AND EACH DECLARES ITS RENDERING.
// `preserve_headers` is a []string written as its JSON text and `strip_prefix` a
// bool written as "0" or "1" — the exact encodings the resource declares in its
// `db:` tags (resreg/codec.go), because the two writers of these items have to
// agree byte for byte. Both are ALWAYS written, empty included: a Go slice and a
// Go bool have no unset, so the zero value renders as its own text ("[]", "0")
// rather than as an absent attribute a reader would have to guess at. A nil
// slice is normalised to the empty one first, so it renders `[]` and never
// `null`.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a route item carries. `path` is NOT among them: it is the
// coordinate.
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

// proxydRouteFrom reads one item back. A `preserve_headers` that is not the JSON
// document it claims to be is a LOUD fault: a route read wrong is a header
// stripped from every request it proxies.
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

// Route ownership (BUGS L45, L46). Every proxyd_routes row is written by one of
// three authorities, and `owner` records which:
//
//	RouteOwnerArizuko  — `arizuko generate` derived it from the release's core
//	                     route table; generate rewrites these on every run.
//	RouteOwnerPackage  — `package:<name>` declared it in its own manifest;
//	                     generate rewrites these too.
//	RouteOwnerOperator — declared through proxyd's /v1/proxyd_routes surface.
//	                     Generate never touches it.
//
// Editing an arizuko- or package-owned row is not an override — the next generate
// rewrites it. Taking a path over is DELETE then POST on /v1/proxyd_routes.
const (
	RouteOwnerArizuko       = "arizuko"
	RouteOwnerOperator      = "operator"
	RouteOwnerPackagePrefix = "package:"
)

// RouteOwnerPackage is the owner value for a row an installed package declared.
// A package may never write `arizuko` or `operator` itself (BUGS L46): the
// first would let it pass as core-shipped, the second would opt it out of every
// later generate, so an upgraded package could not correct its own route.
func RouteOwnerPackage(name string) string { return RouteOwnerPackagePrefix + name }

// RouteSurvivesGenerate reports whether a row with this owner is left alone by
// SyncProxydRoutes. Exactly one owner is: `operator`.
//
// It is one predicate because it was two. The keep loop allowlisted `operator`
// while the PATCH guard denylisted `arizuko`, so a package row passed the guard
// with 200 and was silently overwritten at the next deploy (BUGS L45, L81).
func RouteSurvivesGenerate(owner string) bool { return owner == RouteOwnerOperator }

// ProxydRoute mirrors proxyd's `Route` shape for persistence. Kept here
// without importing proxyd so the store stays a leaf. proxyd converts
// between the two with a one-line copy.
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

// putProxydRouteTx upserts one route on a transaction the caller owns.
//
// It is an UNCONDITIONAL Put, and that is exactly right here: the item carries
// no attribute this file does not own, so replacing the whole set is what the
// `ON CONFLICT(path) DO UPDATE` of every other column said. SyncProxydRoutes
// writes every row through it.
func (s *Store) putProxydRouteTx(ctx context.Context, tx *sql.Tx, r ProxydRoute) error {
	key, err := proxydRouteKey(r.Path)
	if err != nil {
		return err
	}
	attrs, err := proxydRouteAttrs(r)
	if err != nil {
		return err
	}
	_, err = s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: ProxydRoutes, Key: key, Attrs: attrs,
	})
	return err
}

// CreateProxydRouteTx writes ONE route on a transaction the caller owns and
// reports whether it landed — proxyd's `/v1/proxyd_routes` POST and its
// dashboard form, each committing with resreg.invoke's single audit row.
//
// created=false is the PATH ALREADY BEING TAKEN, and it has to be an answer
// rather than an overwrite: the path is the coordinate, so an unconditional Put
// would silently hand an arizuko- or package-owned route to the operator and
// exempt it from every later generate (BUGS L45, L46). Taking a path over is
// DELETE then POST, which is what the surface tells the caller.
func (s *Store) CreateProxydRouteTx(ctx context.Context, tx *sql.Tx, r ProxydRoute) (bool, error) {
	key, err := proxydRouteKey(r.Path)
	if err != nil {
		return false, err
	}
	attrs, err := proxydRouteAttrs(r)
	if err != nil {
		return false, err
	}
	return s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: ProxydRoutes, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
}

// UpdateProxydRouteTx rewrites the mutable half of one route on a transaction
// the caller owns and reports whether a route was there to rewrite.
//
// `owner` is NOT among the fields it writes: ownership is stamped by whoever
// created the row, so an update can never change who may rewrite it. That is
// also why this is an Update and not a Put — a Put carries the item's whole
// attribute set and would take the owner with it.
//
// A field the caller clears is REMOVED rather than set to the empty string,
// because absent is the only null this model has: an empty `gated_by` left
// behind still answers a presence test, and the gate would keep applying to a
// route the operator opened.
func (s *Store) UpdateProxydRouteTx(ctx context.Context, tx *sql.Tx, r ProxydRoute) (bool, error) {
	key, err := proxydRouteKey(r.Path)
	if err != nil {
		return false, err
	}
	attrs, err := proxydRouteAttrs(r)
	if err != nil {
		return false, err
	}
	delete(attrs, attrPxOwner)
	var remove []string
	for _, name := range []string{attrPxBackend, attrPxAuth, attrPxGatedBy, attrPxRedirectTo} {
		if _, set := attrs[name]; !set {
			remove = append(remove, name)
		}
	}
	return s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Update, Entity: ProxydRoutes, Key: key, Attrs: attrs, Remove: remove,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// DeleteProxydRouteTx removes one route on a transaction the caller owns and
// reports whether one went. The answer is load-bearing on both faces: the
// dashboard tells the operator "no route at that path" rather than redirecting
// as if the route were gone.
func (s *Store) DeleteProxydRouteTx(ctx context.Context, tx *sql.Tx, path string) (bool, error) {
	key, err := proxydRouteKey(path)
	if err != nil {
		return false, err
	}
	return s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: ProxydRoutes, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// SyncProxydRoutes writes the release's route table into proxyd_routes and reports
// how many rows it wrote and how many paths it handed to the operator. proxyd reads
// the table live per request, so a route added to a release takes effect on the
// next generate with no restart and no seed-once window (BUGS L45).
//
// Two rules are the whole contract: a path whose row is RouteOwnerOperator is
// SKIPPED whatever it points at, and every other shipped path is overwritten
// keeping the owner the caller stamped.
//
// A package route falls on the ARIZUKO side of that split: it is re-derived from
// the package's own manifest on every generate, so leaving it alone would freeze
// an upgraded package at its first backend. Only the operator's row has no
// upstream to be re-derived from (BUGS L46).
//
// It writes only paths it ships, and never deletes: a route that stops being
// shipped stays until the operator removes it. Audited once for the whole set —
// 18 near-identical rows per generate would bury the mutation that matters.
func (s *Store) SyncProxydRoutes(routes []ProxydRoute) (written, kept int, err error) {
	ctx := context.Background()
	actor, actorSub, surface := s.auditIdentity()
	err = s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		owners, err := s.proxydRouteOwners(ctx)
		if err != nil {
			return audit.Event{}, err
		}
		written, kept = 0, 0
		for _, r := range routes {
			if RouteSurvivesGenerate(owners[r.Path]) {
				kept++
				continue
			}
			if r.Owner == "" {
				r.Owner = RouteOwnerArizuko
			}
			if err := s.putProxydRouteTx(ctx, tx, r); err != nil {
				return audit.Event{}, fmt.Errorf("write route %q: %w", r.Path, err)
			}
			written++
		}
		return audit.Event{
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
		}, nil
	})
	return written, kept, err
}

// proxydRouteOwners reads path→owner for every existing route. It reads through
// the store's own driver rather than the caller's transaction, because an item
// read needs no transaction and the answer it gives — which paths the operator
// has taken over — is a decision made BEFORE the first write of the sweep.
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

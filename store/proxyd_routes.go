package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/audit"
)

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

func (s *Store) AllProxydRoutes() ([]ProxydRoute, error) {
	rows, err := s.db.Query(`SELECT path, backend, auth, gated_by, preserve_headers, strip_prefix, redirect_to, owner
	                         FROM proxyd_routes ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxydRoute
	for rows.Next() {
		var r ProxydRoute
		var headers string
		var strip int
		if err := rows.Scan(&r.Path, &r.Backend, &r.Auth, &r.GatedBy, &headers, &strip, &r.RedirectTo, &r.Owner); err != nil {
			return nil, err
		}
		if headers != "" {
			if err := json.Unmarshal([]byte(headers), &r.PreserveHeaders); err != nil {
				return nil, fmt.Errorf("decode preserve_headers for %q: %w", r.Path, err)
			}
		}
		r.StripPrefix = strip != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// putProxydRouteTx upserts one row by path (delete-then-insert, no unique-
// constraint dependency). The single INSERT every store-side writer shares —
// PutProxydRoute for a package's hot-apply, SyncProxydRoutes for generate.
func putProxydRouteTx(tx *sql.Tx, r ProxydRoute) error {
	headers := "[]"
	if len(r.PreserveHeaders) > 0 {
		b, err := json.Marshal(r.PreserveHeaders)
		if err != nil {
			return err
		}
		headers = string(b)
	}
	strip := 0
	if r.StripPrefix {
		strip = 1
	}
	if _, err := tx.Exec("DELETE FROM proxyd_routes WHERE path=?", r.Path); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO proxyd_routes
		(path, backend, auth, gated_by, preserve_headers, strip_prefix, redirect_to, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Path, r.Backend, r.Auth, r.GatedBy, headers, strip, r.RedirectTo, r.Owner)
	return err
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
	actor, actorSub, surface := s.auditIdentity()
	err = s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		owners, err := proxydRouteOwners(tx)
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
			if err := putProxydRouteTx(tx, r); err != nil {
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

// proxydRouteOwners reads path→owner for every existing row. Drained fully
// before the caller's writes: database/sql forbids an Exec on a tx while one of
// its Query cursors is open.
func proxydRouteOwners(tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.Query("SELECT path, owner FROM proxyd_routes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string]string{}
	for rows.Next() {
		var path, owner string
		if err := rows.Scan(&path, &owner); err != nil {
			return nil, err
		}
		owners[path] = owner
	}
	return owners, rows.Err()
}

// PutProxydRoute upserts a proxyd route by path — the direct-DB write the CLI uses
// to hot-apply a package's route, taking effect without a restart (spec 5/28 P2).
// An unset Owner means RouteOwnerArizuko: the CLI's route writers are the managed
// ones, and only proxyd's own CRUD surface declares an operator route.
//
// Audited: this opens a public URL onto a backend with no restart. ParamsSummary
// carries the blast radius — which backend, behind which auth — not just the path.
func (s *Store) PutProxydRoute(r ProxydRoute) error {
	actor, actorSub, surface := s.auditIdentity()
	if r.Owner == "" {
		r.Owner = RouteOwnerArizuko
	}
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		if err := putProxydRouteTx(tx, r); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "proxyd_route.set",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "proxyd_routes/" + r.Path,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"backend":     r.Backend,
				"auth":        r.Auth,
				"gated_by":    r.GatedBy,
				"redirect_to": r.RedirectTo,
			},
		}, nil
	})
}

// DeleteProxydRoute removes a proxyd route by path. ok=false when absent.
//
// Audited for the same reason as PutProxydRoute, and because a trail that
// records routes opening but never closing reads as "still live" for a route
// that is gone. `deleted` distinguishes the real withdrawal from a no-op.
func (s *Store) DeleteProxydRoute(path string) (bool, error) {
	var hit bool
	actor, actorSub, surface := s.auditIdentity()
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec("DELETE FROM proxyd_routes WHERE path=?", path)
		if err != nil {
			return audit.Event{}, err
		}
		n, _ := res.RowsAffected()
		hit = n > 0
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "proxyd_route.delete",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "proxyd_routes/" + path,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"deleted": hit,
			},
		}, nil
	})
	return hit, err
}

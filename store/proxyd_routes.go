package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/audit"
)

// Route ownership (BUGS L45). Every proxyd_routes row is written by one of two
// authorities, and `owner` records which:
//
//	RouteOwnerArizuko  — `arizuko generate` derived the row from the release's
//	                     core route table plus the installed packages' route
//	                     assets. Generate rewrites these on every run, so a
//	                     route added to a release reaches an existing instance.
//	RouteOwnerOperator — the row was declared through proxyd's
//	                     /v1/proxyd_routes surface. Generate never touches it.
//
// Editing an arizuko-owned row is not an override — the next generate rewrites
// it. Taking a path over is DELETE then POST on /v1/proxyd_routes; the created
// row is operator-owned and generate hands the path off for good.
const (
	RouteOwnerArizuko  = "arizuko"
	RouteOwnerOperator = "operator"
)

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

// SyncProxydRoutes writes the release's route table — `arizuko generate`'s core
// routes plus the installed packages' route assets, already gated_by-filtered —
// into proxyd_routes, and reports how many rows it wrote and how many paths it
// handed to the operator. proxyd reads the table live per request, so a route
// added to a release takes effect on the next generate with no restart and no
// seed-once window (BUGS L45).
//
// Two rules, and they are the whole contract:
//   - a path whose row is RouteOwnerOperator is SKIPPED, whatever it points at;
//   - every other shipped path is overwritten and stamped RouteOwnerArizuko.
//
// It writes only paths it ships. A route the operator added at a path arizuko
// does not ship is not in `routes`, so it is never read, rewritten or deleted.
// The cost of never deleting: a route that stops being shipped, or whose
// gated_by env has since been turned off, stays until the operator removes it.
//
// Audited once for the whole set rather than per route — the mutation an
// operator needs in the trail is "a deploy rewrote the route table", and 18
// near-identical rows per generate would bury it.
func (s *Store) SyncProxydRoutes(routes []ProxydRoute) (written, kept int, err error) {
	actor, actorSub, surface := s.auditIdentity()
	err = s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		owners, err := proxydRouteOwners(tx)
		if err != nil {
			return audit.Event{}, err
		}
		written, kept = 0, 0
		for _, r := range routes {
			if owners[r.Path] == RouteOwnerOperator {
				kept++
				continue
			}
			r.Owner = RouteOwnerArizuko
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

// PutProxydRoute upserts a proxyd route by path. This is the direct-DB write the
// CLI uses to hot-apply a package's route: proxyd reads proxyd_routes live per
// request, so the route takes effect without a restart (spec 5/28 P2, resolving
// 5/27 C2).
//
// An unset Owner means RouteOwnerArizuko: the CLI's route writers are the
// managed ones (a package's routes are re-derived from its `*-routes.json` on
// every generate), and only proxyd's own CRUD surface declares an operator
// route.
//
// Audited: this opens a public URL onto a backend with no restart, and the same
// mutation through proxyd's /v1/proxyd_routes resource already records one
// (resreg emits in the handler's tx). ParamsSummary carries the blast radius —
// which backend, behind which auth — not just the path.
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

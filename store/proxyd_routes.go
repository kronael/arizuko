package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/audit"
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
}

func (s *Store) AllProxydRoutes() ([]ProxydRoute, error) {
	rows, err := s.db.Query(`SELECT path, backend, auth, gated_by, preserve_headers, strip_prefix
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
		if err := rows.Scan(&r.Path, &r.Backend, &r.Auth, &r.GatedBy, &headers, &strip); err != nil {
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

func proxydRouteFields(r ProxydRoute) (headers string, strip int) {
	b, _ := json.Marshal(r.PreserveHeaders)
	if r.PreserveHeaders == nil {
		b = []byte("[]")
	}
	return string(b), btoi(r.StripPrefix)
}

func (s *Store) InsertProxydRoute(r ProxydRoute) error {
	headers, strip := proxydRouteFields(r)
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(`INSERT INTO proxyd_routes
		                     (path, backend, auth, gated_by, preserve_headers, strip_prefix)
		                     VALUES (?, ?, ?, ?, ?, ?)`,
			r.Path, r.Backend, r.Auth, r.GatedBy, headers, strip)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "route.create",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "proxyd_routes/" + r.Path,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"backend":  r.Backend,
				"auth":     r.Auth,
				"gated_by": r.GatedBy,
			},
		}, err
	})
}

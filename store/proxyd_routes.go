package store

import (
	"encoding/json"
	"fmt"
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
}

func (s *Store) AllProxydRoutes() ([]ProxydRoute, error) {
	rows, err := s.db.Query(`SELECT path, backend, auth, gated_by, preserve_headers, strip_prefix, redirect_to
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
		if err := rows.Scan(&r.Path, &r.Backend, &r.Auth, &r.GatedBy, &headers, &strip, &r.RedirectTo); err != nil {
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

// PutProxydRoute upserts a proxyd route by path via delete-then-insert (no
// unique-constraint dependency). This is the direct-DB write the CLI uses to
// hot-apply a package's route: proxyd reads proxyd_routes live per request, so
// the route takes effect without a restart (spec 5/28 P2, resolving 5/27 C2).
func (s *Store) PutProxydRoute(r ProxydRoute) error {
	headers := ""
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM proxyd_routes WHERE path=?", r.Path); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO proxyd_routes
		(path, backend, auth, gated_by, preserve_headers, strip_prefix, redirect_to)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Path, r.Backend, r.Auth, r.GatedBy, headers, strip, r.RedirectTo); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteProxydRoute removes a proxyd route by path. ok=false when absent.
func (s *Store) DeleteProxydRoute(path string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM proxyd_routes WHERE path=?", path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

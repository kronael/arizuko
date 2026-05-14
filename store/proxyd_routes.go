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

func (s *Store) GetProxydRoute(path string) (ProxydRoute, bool) {
	row := s.db.QueryRow(`SELECT path, backend, auth, gated_by, preserve_headers, strip_prefix
	                      FROM proxyd_routes WHERE path = ?`, path)
	var r ProxydRoute
	var headers string
	var strip int
	if err := row.Scan(&r.Path, &r.Backend, &r.Auth, &r.GatedBy, &headers, &strip); err != nil {
		return ProxydRoute{}, false
	}
	if headers != "" {
		_ = json.Unmarshal([]byte(headers), &r.PreserveHeaders)
	}
	r.StripPrefix = strip != 0
	return r, true
}

func (s *Store) InsertProxydRoute(r ProxydRoute) error {
	headers, _ := json.Marshal(r.PreserveHeaders)
	if r.PreserveHeaders == nil {
		headers = []byte("[]")
	}
	strip := 0
	if r.StripPrefix {
		strip = 1
	}
	_, err := s.db.Exec(`INSERT INTO proxyd_routes
	                     (path, backend, auth, gated_by, preserve_headers, strip_prefix)
	                     VALUES (?, ?, ?, ?, ?, ?)`,
		r.Path, r.Backend, r.Auth, r.GatedBy, string(headers), strip)
	return err
}

func (s *Store) UpdateProxydRoute(r ProxydRoute) error {
	headers, _ := json.Marshal(r.PreserveHeaders)
	if r.PreserveHeaders == nil {
		headers = []byte("[]")
	}
	strip := 0
	if r.StripPrefix {
		strip = 1
	}
	res, err := s.db.Exec(`UPDATE proxyd_routes
	                       SET backend=?, auth=?, gated_by=?, preserve_headers=?, strip_prefix=?
	                       WHERE path=?`,
		r.Backend, r.Auth, r.GatedBy, string(headers), strip, r.Path)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("proxyd route %q not found", r.Path)
	}
	return nil
}

func (s *Store) DeleteProxydRoute(path string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM proxyd_routes WHERE path = ?`, path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountProxydRoutes returns total row count; proxyd checks this at boot to
// decide whether to seed from PROXYD_ROUTES_JSON.
func (s *Store) CountProxydRoutes() (int, error) {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM proxyd_routes`)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

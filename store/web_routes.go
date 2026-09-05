package store

import (
	"time"
)

// web_routes is routd's table. routd owns every write (`web_routes_resource.go`,
// on resreg's transaction) and store serves the one read proxyd needs: the whole
// inventory, which proxyd snapshots and then longest-prefix-matches in Go
// (`proxyd/main.go:159`). There is deliberately no writer and no matcher here —
// a second writer is how `created_at` came to mean two different things.

type WebRoute struct {
	PathPrefix string
	Access     string // public | auth | deny | redirect
	RedirectTo string
	Folder     string
	CreatedAt  time.Time
}

// webRouteCols is the projection scanWebRoute expects. One list, one scanner.
const webRouteCols = `path_prefix, access, COALESCE(redirect_to,''), folder, created_at`

func scanWebRoute(r rowScanner) (WebRoute, error) {
	var wr WebRoute
	var createdAt string
	if err := r.Scan(&wr.PathPrefix, &wr.Access, &wr.RedirectTo, &wr.Folder, &createdAt); err != nil {
		return WebRoute{}, err
	}
	wr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return wr, nil
}

// AllWebRoutes returns every web_route, ordered by prefix. proxyd's snapshot.
func (s *Store) AllWebRoutes() ([]WebRoute, error) {
	rows, err := s.db.Query(`SELECT ` + webRouteCols + ` FROM web_routes ORDER BY path_prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebRoute
	for rows.Next() {
		wr, err := scanWebRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wr)
	}
	return out, rows.Err()
}

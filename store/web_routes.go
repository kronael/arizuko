package store

import "time"

// WebRoute is a row from the web_routes table.
type WebRoute struct {
	PathPrefix string
	Access     string // public | auth | deny | redirect
	RedirectTo string
	Folder     string
	CreatedAt  time.Time
}

// SetWebRoute upserts a web_route row.
func (s *Store) SetWebRoute(r WebRoute) error {
	_, err := s.db.Exec(
		`INSERT INTO web_routes (path_prefix, access, redirect_to, folder, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(path_prefix) DO UPDATE SET
		   access = excluded.access,
		   redirect_to = excluded.redirect_to,
		   folder = excluded.folder,
		   created_at = excluded.created_at`,
		r.PathPrefix, r.Access, nilIfEmpty(r.RedirectTo), r.Folder,
		r.CreatedAt.UTC().Format(time.RFC3339),
	)
	return err
}

// DelWebRoute deletes the web_route with the given path_prefix.
func (s *Store) DelWebRoute(pathPrefix string) error {
	_, err := s.db.Exec(`DELETE FROM web_routes WHERE path_prefix = ?`, pathPrefix)
	return err
}

// ListWebRoutes returns all web_routes rows owned by folder.
func (s *Store) ListWebRoutes(folder string) []WebRoute {
	rows, err := s.db.Query(
		`SELECT path_prefix, access, COALESCE(redirect_to,''), folder, created_at
		 FROM web_routes WHERE folder = ? ORDER BY path_prefix`,
		folder,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []WebRoute
	for rows.Next() {
		var r WebRoute
		var createdAt string
		if err := rows.Scan(&r.PathPrefix, &r.Access, &r.RedirectTo, &r.Folder, &createdAt); err == nil {
			r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
			out = append(out, r)
		}
	}
	return out
}

// AllWebRoutes returns every web_route row (used by proxyd cache).
func (s *Store) AllWebRoutes() []WebRoute {
	rows, err := s.db.Query(
		`SELECT path_prefix, access, COALESCE(redirect_to,''), folder, created_at
		 FROM web_routes ORDER BY path_prefix`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []WebRoute
	for rows.Next() {
		var r WebRoute
		var createdAt string
		if err := rows.Scan(&r.PathPrefix, &r.Access, &r.RedirectTo, &r.Folder, &createdAt); err == nil {
			r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
			out = append(out, r)
		}
	}
	return out
}

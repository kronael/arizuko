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

func scanWebRoute(r rowScanner) (WebRoute, error) {
	var wr WebRoute
	var createdAt string
	if err := r.Scan(&wr.PathPrefix, &wr.Access, &wr.RedirectTo, &wr.Folder, &createdAt); err != nil {
		return WebRoute{}, err
	}
	wr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return wr, nil
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

// DelWebRoute deletes a web_route owned by folder. Returns false if not found.
func (s *Store) DelWebRoute(pathPrefix, folder string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM web_routes WHERE path_prefix = ? AND (folder = ? OR ? = '')`,
		pathPrefix, folder, folder,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
		if wr, err := scanWebRoute(rows); err == nil {
			out = append(out, wr)
		}
	}
	return out
}

// MatchWebRoute returns the longest-prefix web_route matching urlPath, if any.
func (s *Store) MatchWebRoute(urlPath string) (WebRoute, bool) {
	row := s.db.QueryRow(
		`SELECT path_prefix, access, COALESCE(redirect_to,''), folder, created_at
		 FROM web_routes
		 WHERE ? LIKE path_prefix || '%'
		 ORDER BY length(path_prefix) DESC
		 LIMIT 1`,
		urlPath,
	)
	wr, err := scanWebRoute(row)
	return wr, err == nil
}

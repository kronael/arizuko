package store

import (
	"database/sql"
	"time"

	"github.com/kronael/arizuko/audit"
)

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

func (s *Store) SetWebRoute(r WebRoute) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
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
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "web_route.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "web_routes/" + r.PathPrefix,
			Folder:   r.Folder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"access":      r.Access,
				"redirect_to": r.RedirectTo,
			},
		}, err
	})
}

func (s *Store) DelWebRoute(pathPrefix, folder string) (bool, error) {
	var hit bool
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec(
			`DELETE FROM web_routes WHERE path_prefix = ? AND (folder = ? OR ? = '')`,
			pathPrefix, folder, folder,
		)
		if err != nil {
			return audit.Event{}, err
		}
		n, _ := res.RowsAffected()
		hit = n > 0
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "web_route.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "web_routes/" + pathPrefix,
			Folder:   folder,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
	return hit, err
}

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

func (s *Store) AllWebRoutes() ([]WebRoute, error) {
	rows, err := s.db.Query(
		`SELECT path_prefix, access, COALESCE(redirect_to,''), folder, created_at
		 FROM web_routes ORDER BY path_prefix`,
	)
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

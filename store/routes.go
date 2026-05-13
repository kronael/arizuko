package store

import (
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

const routeCols = `id, seq, match, target, COALESCE(impulse_config,'')`

func scanRoute(r rowScanner) (core.Route, error) {
	var rt core.Route
	err := r.Scan(&rt.ID, &rt.Seq, &rt.Match, &rt.Target, &rt.ImpulseConfig)
	return rt, err
}

func (s *Store) AllRoutes() []core.Route {
	rows, err := s.db.Query(`SELECT ` + routeCols + ` FROM routes ORDER BY seq ASC, id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Route
	for rows.Next() {
		if r, err := scanRoute(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) GetRoute(id int64) (core.Route, bool) {
	r, err := scanRoute(s.db.QueryRow(`SELECT `+routeCols+` FROM routes WHERE id = ?`, id))
	return r, err == nil
}

func (s *Store) AddRoute(r core.Route) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO routes (seq, match, target, impulse_config) VALUES (?, ?, ?, ?)`,
		r.Seq, r.Match, r.Target, nilIfEmpty(r.ImpulseConfig),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRoutes replaces the routes whose target is `folder` or `folder/...`.
func (s *Store) SetRoutes(folder string, routes []core.Route) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM routes WHERE target = ? OR target LIKE ?||'/%'`,
		folder, folder,
	); err != nil {
		return err
	}
	for _, r := range routes {
		if _, err := tx.Exec(
			`INSERT INTO routes (seq, match, target, impulse_config) VALUES (?, ?, ?, ?)`,
			r.Seq, r.Match, r.Target, nilIfEmpty(r.ImpulseConfig),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteRoute(id int64) error {
	_, err := s.db.Exec(`DELETE FROM routes WHERE id = ?`, id)
	return err
}

func (s *Store) ListRoutes(folder string, isRoot bool) []core.Route {
	scope := folder
	if isRoot {
		scope = ""
	}
	rows, err := s.db.Query(
		`SELECT `+routeCols+` FROM routes
		 WHERE (? = '' OR target = ? OR target LIKE ?||'/%')
		 ORDER BY seq, id`, scope, scope, scope)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Route
	for rows.Next() {
		if r, err := scanRoute(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// GetImpulseConfigJSON walks routes in seq order and returns the first
// non-empty impulse_config whose match expression evaluates true for msg.
func (s *Store) GetImpulseConfigJSON(msg core.Message) string {
	for _, r := range s.AllRoutes() {
		if r.ImpulseConfig == "" {
			continue
		}
		if router.RouteMatches(r, msg) {
			return r.ImpulseConfig
		}
	}
	return ""
}

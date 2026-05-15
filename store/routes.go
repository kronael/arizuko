package store

import (
	"database/sql"

	"github.com/kronael/arizuko/core"
)

const routeCols = `id, seq, match, target,
	COALESCE(observe_window_messages, 0), COALESCE(observe_window_chars, 0)`

func scanRoute(r rowScanner) (core.Route, error) {
	var rt core.Route
	err := r.Scan(&rt.ID, &rt.Seq, &rt.Match, &rt.Target,
		&rt.ObserveWindowMessages, &rt.ObserveWindowChars)
	return rt, err
}

func (s *Store) AllRoutes() []core.Route {
	rows, err := s.db.Query(`SELECT ` + routeCols + ` FROM routes ORDER BY seq ASC, id ASC`)
	if err != nil {
		return nil
	}
	return collectRoutes(rows)
}

func (s *Store) GetRoute(id int64) (core.Route, bool) {
	r, err := scanRoute(s.db.QueryRow(`SELECT `+routeCols+` FROM routes WHERE id = ?`, id))
	return r, err == nil
}

func (s *Store) AddRoute(r core.Route) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO routes (seq, match, target, observe_window_messages, observe_window_chars)
		 VALUES (?, ?, ?, ?, ?)`,
		r.Seq, r.Match, r.Target,
		nilIfZero(r.ObserveWindowMessages), nilIfZero(r.ObserveWindowChars),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRoutes replaces the routes whose target (sans fragment) is `folder`
// or under `folder/`.
func (s *Store) SetRoutes(folder string, routes []core.Route) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM routes
		 WHERE target = ? OR target LIKE ?||'#%'
		    OR target LIKE ?||'/%'`,
		folder, folder, folder,
	); err != nil {
		return err
	}
	for _, r := range routes {
		if _, err := tx.Exec(
			`INSERT INTO routes (seq, match, target, observe_window_messages, observe_window_chars)
			 VALUES (?, ?, ?, ?, ?)`,
			r.Seq, r.Match, r.Target,
			nilIfZero(r.ObserveWindowMessages), nilIfZero(r.ObserveWindowChars),
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
		 WHERE (? = '' OR target = ? OR target LIKE ?||'#%' OR target LIKE ?||'/%')
		 ORDER BY seq, id`, scope, scope, scope, scope)
	if err != nil {
		return nil
	}
	return collectRoutes(rows)
}

func collectRoutes(rows *sql.Rows) []core.Route {
	defer rows.Close()
	var out []core.Route
	for rows.Next() {
		if r, err := scanRoute(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

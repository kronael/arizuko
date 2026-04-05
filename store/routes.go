package store

import (
	"database/sql"
	"strings"

	"github.com/onvos/arizuko/core"
)

func platformPrefix(jid string) string {
	i := strings.Index(jid, ":")
	if i < 0 {
		return jid
	}
	return jid[:i+1]
}

const routeCols = `id, jid, seq, type, COALESCE(match,''), target, COALESCE(impulse_config,'')`

func scanRoute(r rowScanner) (core.Route, error) {
	var rt core.Route
	err := r.Scan(&rt.ID, &rt.JID, &rt.Seq, &rt.Type, &rt.Match, &rt.Target, &rt.ImpulseConfig)
	return rt, err
}

func (s *Store) GetRoutes(jid string) []core.Route {
	prefix := platformPrefix(jid)
	rows, err := s.db.Query(
		`SELECT `+routeCols+` FROM routes WHERE jid = ? OR jid = ?
		 ORDER BY CASE jid WHEN ? THEN 0 ELSE 1 END, seq ASC`,
		jid, prefix, jid)
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
	r, err := scanRoute(s.db.QueryRow(`SELECT ` + routeCols + ` FROM routes WHERE id = ?`, id))
	return r, err == nil
}

func (s *Store) AddRoute(jid string, r core.Route) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO routes (jid, seq, type, match, target, impulse_config) VALUES (?, ?, ?, ?, ?, ?)`,
		jid, r.Seq, r.Type, nilIfEmpty(r.Match), r.Target, nilIfEmpty(r.ImpulseConfig),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) InsertPrefixRoutes(jid, folder string) {
	s.db.Exec(
		`INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
		 VALUES (?, -2, 'prefix', '@', ?), (?, -1, 'prefix', '#', ?)`,
		jid, folder, jid, folder,
	)
}

func (s *Store) SetRoutes(jid string, routes []core.Route) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM routes WHERE jid = ?`, jid); err != nil {
		return err
	}
	for _, r := range routes {
		if _, err := tx.Exec(
			`INSERT INTO routes (jid, seq, type, match, target, impulse_config) VALUES (?, ?, ?, ?, ?, ?)`,
			jid, r.Seq, r.Type, nilIfEmpty(r.Match), r.Target, nilIfEmpty(r.ImpulseConfig),
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
	var rows *sql.Rows
	var err error
	if isRoot {
		rows, err = s.db.Query(`SELECT ` + routeCols + ` FROM routes ORDER BY jid, seq`)
	} else {
		rows, err = s.db.Query(
			`SELECT `+routeCols+` FROM routes
			 WHERE target = ? OR target LIKE ?||'/%'
			 ORDER BY jid, seq`, folder, folder)
	}
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

func (s *Store) GetImpulseConfigJSON(jid string) string {
	prefix := platformPrefix(jid)
	var cfg string
	s.db.QueryRow(
		`SELECT COALESCE(impulse_config, '')
		 FROM routes
		 WHERE (jid = ? OR jid = ?) AND impulse_config IS NOT NULL
		 ORDER BY CASE jid WHEN ? THEN 0 ELSE 1 END
		 LIMIT 1`,
		jid, prefix, jid,
	).Scan(&cfg)
	return cfg
}

func (s *Store) RouteSourceJIDsInWorld(worldFolder string) []string {
	rows, err := s.db.Query(
		`SELECT DISTINCT jid FROM routes
		 WHERE target = ? OR target LIKE ?||'/%'`,
		worldFolder, worldFolder)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		out = append(out, jid)
	}
	return out
}

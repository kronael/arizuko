package store

import (
	"github.com/onvos/arizuko/core"
)

func (s *Store) GetRoutes(jid string) []core.Route {
	rows, err := s.db.Query(
		`SELECT id, jid, seq, type, COALESCE(match, ''), target
		 FROM routes WHERE jid = ? ORDER BY seq ASC`, jid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Route
	for rows.Next() {
		var r core.Route
		rows.Scan(&r.ID, &r.JID, &r.Seq, &r.Type, &r.Match, &r.Target)
		out = append(out, r)
	}
	return out
}

func (s *Store) GetRoute(id int64) (core.Route, bool) {
	var r core.Route
	err := s.db.QueryRow(
		`SELECT id, jid, seq, type, COALESCE(match, ''), target FROM routes WHERE id = ?`, id,
	).Scan(&r.ID, &r.JID, &r.Seq, &r.Type, &r.Match, &r.Target)
	return r, err == nil
}

func (s *Store) AddRoute(jid string, r core.Route) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO routes (jid, seq, type, match, target) VALUES (?, ?, ?, ?, ?)`,
		jid, r.Seq, r.Type, nilIfEmpty(r.Match), r.Target,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertPrefixRoutes inserts @ (seq=-2) and # (seq=-1) prefix routes for jid
// pointing at folder. Negative seq ensures they evaluate before default (seq=0).
// Uses INSERT OR IGNORE to skip duplicates.
// Error is intentionally ignored: prefix routes are a convenience inserted by
// convention and can be added manually if needed. Non-fatal.
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
			`INSERT INTO routes (jid, seq, type, match, target) VALUES (?, ?, ?, ?, ?)`,
			jid, r.Seq, r.Type, nilIfEmpty(r.Match), r.Target,
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

// RouteSourceJIDsInWorld returns distinct source JIDs where target is
// worldFolder or any subfolder of it.
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

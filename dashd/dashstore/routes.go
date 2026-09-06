package dashstore

import (
	"database/sql"
	"errors"
)

// ErrRouteMissing reports that no route carries that id.
var ErrRouteMissing = errors.New("route not found")

// RouteTarget reads the target a route points at today. Both route mutations
// gate admin on it rather than on the target the caller submitted: without it
// an admin on folder B could repoint a route that currently lands in folder A.
// It reports ErrRouteMissing for an absent row, so the caller answers 404 for a
// deleted route and 500 for a store it could not reach — an operator told "not
// found" about a route that still exists would go and recreate it.
func (s *Store) RouteTarget(id int64) (string, error) {
	var target string
	err := s.db.QueryRow(`SELECT target FROM routes WHERE id = ?`, id).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRouteMissing
	}
	return target, err
}

package dashstore

import "errors"

// ErrRouteMissing reports that no route carries that id.
var ErrRouteMissing = errors.New("route not found")

// RouteTarget reads the target a route points at today. Both route mutations
// gate admin on it rather than on the target the caller submitted: without it
// an admin on folder B could repoint a route that currently lands in folder A.
// It reports ErrRouteMissing for an absent row, so the caller answers 404 for a
// deleted route and 500 for a store it could not reach — an operator told "not
// found" about a route that still exists would go and recreate it. That is why
// this reads through store.RouteByID and not GetRoute: GetRoute collapses the
// two answers into one false.
func (s *Store) RouteTarget(id int64) (string, error) {
	r, ok, err := s.st().RouteByID(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrRouteMissing
	}
	return r.Target, nil
}

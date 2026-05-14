package store

import (
	"errors"
	"time"
)

// ErrCycle: adding the edge would create a cycle in acl_membership.
var ErrCycle = errors.New("acl_membership: cycle")

// ErrSelfMembership: child == parent.
var ErrSelfMembership = errors.New("acl_membership: self")

// AddMembership inserts (child → parent) idempotently. Rejects
// self-membership and any edge that would close a cycle.
//
// Cycle test: starting from `parent`, walk upward via acl_membership
// (parent's parents, ...). If `child` appears in the closure, the new
// edge would form a cycle.
func (s *Store) AddMembership(child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Recursive walk: would the new edge make `child` reachable from `parent`?
	var hits int
	err = tx.QueryRow(
		`WITH RECURSIVE up(p) AS (
		   SELECT ? UNION
		   SELECT acl_membership.parent FROM acl_membership
		     JOIN up ON acl_membership.child = up.p
		 )
		 SELECT COUNT(*) FROM up WHERE p = ?`,
		parent, child,
	).Scan(&hits)
	if err != nil {
		return err
	}
	if hits > 0 {
		return ErrCycle
	}

	var grantedBy any
	if addedBy != "" {
		grantedBy = addedBy
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at)
		 VALUES (?, ?, ?, ?)`,
		child, parent, grantedBy, time.Now().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveMembership(child, parent string) error {
	_, err := s.db.Exec(
		`DELETE FROM acl_membership WHERE child = ? AND parent = ?`,
		child, parent,
	)
	return err
}

// Members returns direct children of `parent`.
func (s *Store) Members(parent string) []string {
	rows, err := s.db.Query(
		`SELECT child FROM acl_membership WHERE parent = ? ORDER BY child`, parent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// Ancestors returns the transitive closure of parents reachable from
// `child` via acl_membership, excluding `child` itself. Cycle-safe by
// the UNION (recursive CTE deduplicates).
func (s *Store) Ancestors(child string) []string {
	rows, err := s.db.Query(
		`WITH RECURSIVE up(p) AS (
		   SELECT parent FROM acl_membership WHERE child = ?
		   UNION
		   SELECT acl_membership.parent FROM acl_membership
		     JOIN up ON acl_membership.child = up.p
		 )
		 SELECT p FROM up ORDER BY p`,
		child,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil && v != child {
			out = append(out, v)
		}
	}
	return out
}

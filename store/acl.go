package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
)

// AddACLRow inserts a row idempotently. (principal, action, scope, params,
// predicate, effect) is the primary key.
func (s *Store) AddACLRow(row core.ACLRow) error {
	if row.Effect == "" {
		row.Effect = "allow"
	}
	if row.GrantedAt == "" {
		row.GrantedAt = time.Now().Format(time.RFC3339)
	}
	var grantedBy any
	if row.GrantedBy != "" {
		grantedBy = row.GrantedBy
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO acl
		  (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.Principal, row.Action, row.Scope, row.Effect,
		row.Params, row.Predicate, grantedBy, row.GrantedAt,
	)
	return err
}

func (s *Store) RemoveACLRow(row core.ACLRow) error {
	if row.Effect == "" {
		row.Effect = "allow"
	}
	_, err := s.db.Exec(
		`DELETE FROM acl
		 WHERE principal = ? AND action = ? AND scope = ?
		   AND params = ? AND predicate = ? AND effect = ?`,
		row.Principal, row.Action, row.Scope,
		row.Params, row.Predicate, row.Effect,
	)
	return err
}

func scanACLRow(rows *sql.Rows) (core.ACLRow, error) {
	var r core.ACLRow
	var grantedBy sql.NullString
	err := rows.Scan(
		&r.Principal, &r.Action, &r.Scope, &r.Effect,
		&r.Params, &r.Predicate, &grantedBy, &r.GrantedAt,
	)
	if grantedBy.Valid {
		r.GrantedBy = grantedBy.String
	}
	return r, err
}

const aclCols = `principal, action, scope, effect, params, predicate, granted_by, granted_at`

// ACLRowsFor returns rows whose principal EXACTLY matches any element of
// principals. Wildcard-bearing stored principals are fetched separately
// via ACLWildcardRows.
func (s *Store) ACLRowsFor(principals []string) []core.ACLRow {
	if len(principals) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(principals))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(principals))
	for _, p := range principals {
		args = append(args, p)
	}
	rows, err := s.db.Query(
		`SELECT `+aclCols+` FROM acl WHERE principal IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.ACLRow
	for rows.Next() {
		r, err := scanACLRow(rows)
		if err == nil {
			out = append(out, r)
		}
	}
	return out
}

// UserScopes returns the distinct allow-scopes the sub has access to,
// after expanding membership transitively. Used for building JWT group
// claims and webdav landing. Equivalent of the legacy UserGroups()
// pattern list, sourced from the unified acl/acl_membership tables.
func (s *Store) UserScopes(sub string) []string {
	if sub == "" {
		return nil
	}
	principals := append([]string{sub}, s.Ancestors(sub)...)
	if len(principals) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(principals))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(principals))
	for _, p := range principals {
		args = append(args, p)
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT scope FROM acl
		 WHERE effect='allow' AND principal IN (`+placeholders+`)
		 ORDER BY scope`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err == nil && sc != "" {
			out = append(out, sc)
		}
	}
	return out
}

// ListACL returns every acl row, optionally filtered by principal.
// Empty principal returns all rows.
func (s *Store) ListACL(principal string) []core.ACLRow {
	q := `SELECT ` + aclCols + ` FROM acl`
	args := []any{}
	if principal != "" {
		q += ` WHERE principal = ?`
		args = append(args, principal)
	}
	q += ` ORDER BY principal, action, scope`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.ACLRow
	for rows.Next() {
		r, err := scanACLRow(rows)
		if err == nil {
			out = append(out, r)
		}
	}
	return out
}

// ACLWildcardRows returns rows whose stored principal contains a glob
// segment (`*`). Evaluated by Authorize as a second pass against the
// expanded principal set.
func (s *Store) ACLWildcardRows() []core.ACLRow {
	rows, err := s.db.Query(
		`SELECT ` + aclCols + ` FROM acl WHERE principal LIKE '%*%'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.ACLRow
	for rows.Next() {
		r, err := scanACLRow(rows)
		if err == nil {
			out = append(out, r)
		}
	}
	return out
}

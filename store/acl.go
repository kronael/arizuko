package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/kronael/arizuko/audit"
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
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO acl
		  (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.Principal, row.Action, row.Scope, row.Effect,
		row.Params, row.Predicate, grantedBy, row.GrantedAt, boolToInt(row.GrantOption),
	); err != nil {
		return err
	}
	// GrantedBy is the grantor recorded in the row itself; it stands in as the
	// actor for callers that never set AsUser (the CLI, onbod invite-accept).
	actor, actorSub, surface := s.auditIdentity()
	if s.auditSub == "" && row.GrantedBy != "" {
		actor, actorSub = row.GrantedBy, row.GrantedBy
	}
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategoryAuthZ,
		Action:   "acl.add",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("acl/%s/%s/%s", row.Principal, row.Action, row.Scope),
		Folder:   row.Scope,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"principal": row.Principal,
			"action":    row.Action,
			"scope":     row.Scope,
			"effect":    row.Effect,
			"predicate": row.Predicate,
		},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveACLRow(row core.ACLRow) error {
	if row.Effect == "" {
		row.Effect = "allow"
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM acl
		 WHERE principal = ? AND action = ? AND scope = ?
		   AND params = ? AND predicate = ? AND effect = ?`,
		row.Principal, row.Action, row.Scope,
		row.Params, row.Predicate, row.Effect,
	); err != nil {
		return err
	}
	actor, actorSub, surface := s.auditIdentity()
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategoryAuthZ,
		Action:   "acl.remove",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("acl/%s/%s/%s", row.Principal, row.Action, row.Scope),
		Folder:   row.Scope,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"principal": row.Principal,
			"action":    row.Action,
			"scope":     row.Scope,
			"effect":    row.Effect,
		},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// PutACLRow inserts an acl row idempotently WITHOUT emitting an audit_log row —
// the audit-free twin of AddACLRow, for a caller with no audit context of its
// own. Writing acl into routd.db is NOT such a case: AddACLRow emits with
// audit.EmitInTx, which lands in the tx's own DB, and routd.db has had audit_log
// since routd migration 0016. Same INSERT OR IGNORE on the (principal, action,
// scope, params, predicate, effect) key.
func (s *Store) PutACLRow(row core.ACLRow) error {
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
		  (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.Principal, row.Action, row.Scope, row.Effect,
		row.Params, row.Predicate, grantedBy, row.GrantedAt, boolToInt(row.GrantOption))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RemoveACLRowBare deletes an acl row WITHOUT emitting an audit_log row — the
// audit-free twin of RemoveACLRow used by the FS-mounted daemons (dashd grants
// admin, the CLI) that write acl into routd.db directly. routd itself uses the
// audited RemoveACLRow.
func (s *Store) RemoveACLRowBare(row core.ACLRow) error {
	if row.Effect == "" {
		row.Effect = "allow"
	}
	_, err := s.db.Exec(
		`DELETE FROM acl
		 WHERE principal = ? AND action = ? AND scope = ?
		   AND params = ? AND predicate = ? AND effect = ?`,
		row.Principal, row.Action, row.Scope,
		row.Params, row.Predicate, row.Effect)
	return err
}

func scanACLRow(rows *sql.Rows) (core.ACLRow, error) {
	var r core.ACLRow
	var grantedBy sql.NullString
	var grantOption int
	err := rows.Scan(
		&r.Principal, &r.Action, &r.Scope, &r.Effect,
		&r.Params, &r.Predicate, &grantedBy, &r.GrantedAt, &grantOption,
	)
	if grantedBy.Valid {
		r.GrantedBy = grantedBy.String
	}
	r.GrantOption = grantOption != 0
	return r, err
}

const aclCols = `principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option`

// ACLRowsFor returns rows whose principal EXACTLY matches any element of
// principals. Wildcard-bearing stored principals are fetched separately
// via ACLWildcardRows.
func (s *Store) ACLRowsFor(principals []string) []core.ACLRow {
	if len(principals) == 0 {
		return nil
	}
	args := make([]any, 0, len(principals))
	for _, p := range principals {
		args = append(args, p)
	}
	rows, err := s.db.Query(
		`SELECT `+aclCols+` FROM acl WHERE principal IN (`+sqlPH(len(principals))+`)`, args...)
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
	if err := rows.Err(); err != nil {
		slog.Error("ACLRowsFor iteration failed; failing closed", "err", err)
		return nil
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
	args := make([]any, 0, len(principals))
	for _, p := range principals {
		args = append(args, p)
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT scope FROM acl
		 WHERE effect='allow' AND principal IN (`+sqlPH(len(principals))+`)
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

// ListACLByScope returns all acl rows whose scope exactly matches scope.
func (s *Store) ListACLByScope(scope string) []core.ACLRow {
	rows, err := s.db.Query(
		`SELECT `+aclCols+` FROM acl WHERE scope = ? ORDER BY principal, action`, scope)
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
	if err := rows.Err(); err != nil {
		slog.Error("ListACLByScope iteration failed; failing closed", "err", err)
		return nil
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
	if err := rows.Err(); err != nil {
		slog.Error("ListACL iteration failed; failing closed", "err", err)
		return nil
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
	if err := rows.Err(); err != nil {
		slog.Error("ACLWildcardRows iteration failed; failing closed", "err", err)
		return nil
	}
	return out
}

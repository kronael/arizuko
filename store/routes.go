package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
)

// ErrWebJIDRouted is returned by AddRoute when the operator tries to
// register a chat_jid=web:* predicate. Web JIDs are 1:1 with groups —
// the gateway addresses web:<folder> directly via GroupByFolder and
// never consults the route table. The fix is to create the group, not
// add a route.
var ErrWebJIDRouted = fmt.Errorf(
	"web: JIDs are 1:1 with groups; route table does not apply. " +
		"Create the group via setup_group instead.",
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
	if matchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO routes (seq, match, target, observe_window_messages, observe_window_chars)
		 VALUES (?, ?, ?, ?, ?)`,
		r.Seq, r.Match, r.Target,
		nilIfZero(r.ObserveWindowMessages), nilIfZero(r.ObserveWindowChars),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	actor, actorSub, surface := s.auditIdentity()
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategoryMutation,
		Action:   "route.create",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("routes/%d", id),
		Folder:   r.Target,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"match":  r.Match,
			"target": r.Target,
			"seq":    r.Seq,
		},
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteRoute removes a route by id and records it, the audited twin of
// DeleteRouteRow. Returns rows affected (0 → not found, and no audit row: a
// delete that matched nothing is not a mutation). Mirrors AddRoute — the route
// and its audit row commit together, so an audit row cannot outlive a rolled
// back delete.
func (s *Store) DeleteRoute(id int64) (int64, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Read the target before deleting: it is the audit row's Folder, and after
	// the DELETE there is nothing left to resolve it from.
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT target FROM routes WHERE id = ?`, id).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	actor, actorSub, surface := s.auditIdentity()
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategoryMutation,
		Action:   "route.delete",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("routes/%d", id),
		Folder:   core.ParseRouteTarget(target).Folder,
		Outcome:  audit.OutcomeOK,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// PutRouteRow inserts a route idempotently WITHOUT emitting an audit_log row —
// the audit-free twin of AddRoute, for a caller with no audit context of its
// own. Mirrors AddRoute's full column set (incl. observe_window_messages/
// observe_window_chars) so no column is silently dropped.
func (s *Store) PutRouteRow(r core.Route) (int64, error) {
	if matchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO routes (seq, match, target, observe_window_messages, observe_window_chars)
		 VALUES (?, ?, ?, ?, ?)`,
		r.Seq, r.Match, r.Target,
		nilIfZero(r.ObserveWindowMessages), nilIfZero(r.ObserveWindowChars),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateRouteRow updates a route by id WITHOUT emitting an audit_log row, for a
// caller that emits its own (dashd's handler does). Mirrors AddRoute's full
// column set so the observe-window columns are not silently dropped on edit.
// Returns the number of rows affected (0 → not found).
func (s *Store) UpdateRouteRow(id int64, r core.Route) (int64, error) {
	if matchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	res, err := s.db.Exec(
		`UPDATE routes SET seq = ?, match = ?, target = ?,
		   observe_window_messages = ?, observe_window_chars = ?
		 WHERE id = ?`,
		r.Seq, r.Match, r.Target,
		nilIfZero(r.ObserveWindowMessages), nilIfZero(r.ObserveWindowChars), id,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRouteRow removes a route by id WITHOUT emitting an audit_log row — the
// audit-free twin of DeleteRoute, for a caller with no audit context of its own.
// Returns rows affected (0 → not found).
func (s *Store) DeleteRouteRow(id int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM routes WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// matchesWebJID reports whether a route's Match expression includes a
// chat_jid predicate that begins with "web:". Such routes can never
// fire — the gateway addresses web JIDs directly — and silently mask
// the operator's setup error.
func matchesWebJID(match string) bool {
	for f := range strings.FieldsSeq(match) {
		k, pat, ok := strings.Cut(f, "=")
		if !ok || k != "chat_jid" {
			continue
		}
		if strings.HasPrefix(pat, "web:") {
			return true
		}
	}
	return false
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

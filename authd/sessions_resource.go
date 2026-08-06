package main

// sessions_resource.go publishes refresh-token FAMILIES at GET /v1/sessions and
// kills one at DELETE /v1/sessions/{family_id}.
//
// It closes the other two of spec 5/1's open items. Before it, `FROM
// refresh_tokens` had exactly ONE reader in the tree — lookupRefresh, `WHERE
// token_hash = ?` — so there was no way to ask who was logged in; and
// `revokeFamily` fired only from reuse detection and from the user's own
// `/auth/logout` cookie, so an operator holding a compromised account could not
// cut the session off. Self-service logout was never the gap; admin-initiated
// revocation was (BUGS F15).
//
// WHERE THE AUDIT ROW LANDS, which BUGS F15a called out as undecided: auth.db,
// written by resreg.invoke INSIDE the revoke's own transaction, and readable
// because 5/I federated authd's audit_log into /dash/audit/. The objection at
// the time — "an operator-initiated revoke recorded only in auth.db is
// invisible on the page that exists to show it" — was true when it was written
// and is not true now, which is what unblocks this endpoint rather than any
// argument about it.
//
// Taking resreg's standard `delete` action is what buys that row: invoke opens
// the transaction, hands it to the handler, and emits the audit event into the
// same one, rolling the mutation back if the audit write fails. A bespoke
// POST /revoke would have had to hand-roll all three.

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// sessionsResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/sessions.go).
func (s *server) sessionsResource() resreg.Resource {
	return resreg.Resource{
		Name:      "sessions",
		Endpoints: resources.SessionsEndpoints,
		Authz: func(_ resreg.Caller, action resreg.Action, args resreg.Args) (string, map[string]string, error) {
			return "", map[string]string{"family_id": args.Str("family_id")}, nil
		},
		Gate:    s.sessionsGate,
		Handler: s.sessionsHandler,
		Store:   s.auditStore(),
	}
}

func (s *server) mountSessions(m *http.ServeMux) {
	resreg.RegisterREST(m, s.sessionsResource(), s.auditCaller)
}

// sessionsGate splits read from write. Listing who is logged in and ending
// someone's session are different authorities, so they are different scopes —
// a dashboard that only renders the table never needs the kill verb.
func (s *server) sessionsGate(x resreg.Execution, scope string, params map[string]string) error {
	verb := "read"
	if x.Action.Mutates() {
		verb = "write"
	}
	return instanceWideGate("sessions", verb)(x, scope, params)
}

func (s *server) sessionsHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		rows, err := selectSessions(s.a.db, x.Args.Str("sub"), int(x.Args.Int("limit")), time.Now())
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return rows, nil
	case resreg.ActionDelete:
		return s.revokeSession(x)
	default:
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
}

// revokeSession kills one family inside the transaction resreg opened, so the
// revoke and its audit row commit together or not at all.
//
// A family_id that matches nothing is a 404 rather than a silent success: an
// operator who mistypes an id during an incident must not be told the session
// is dead. `revoked_at IS NULL` in the UPDATE means re-revoking an already-dead
// family also 404s, which is the same honest answer — nothing was killed here.
func (s *server) revokeSession(x resreg.Execution) (any, error) {
	fam := x.Args.Str("family_id")
	if fam == "" {
		return nil, resreg.Errorf(http.StatusBadRequest, "family_id required")
	}
	res, err := x.Tx.Exec(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`,
		now(), fam)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if n == 0 {
		return nil, resreg.Errorf(http.StatusNotFound, "no live session %q", fam)
	}
	return map[string]any{"family_id": fam, "revoked": n}, nil
}

// maxSessionsLimit caps one page, matching audit's own ceiling.
const maxSessionsLimit = 200

// selectSessions projects one row per family off refresh_tokens.
//
// The family's CURRENT row is the one with `used_at IS NULL`, and there is
// exactly one per family by construction: issueRefresh starts a family with a
// single unused row, and each rotation marks the presented row used while
// inserting one unused successor — atomically, since F36. That invariant is
// what lets this be a plain WHERE instead of a window function, and
// TestSessionsRowPerFamilyInvariant pins it.
//
// `token_hash` appears in no SELECT list here. The `sub` filter is a bound
// parameter, never interpolated.
func selectSessions(db *sql.DB, sub string, limit int, at time.Time) ([]resources.SessionsRow, error) {
	if limit <= 0 || limit > maxSessionsLimit {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT r.family_id, r.sub, r.scope, r.aud, r.issued_at, r.expires_at, r.revoked_at,
		        (SELECT COUNT(*)        FROM refresh_tokens f WHERE f.family_id = r.family_id),
		        (SELECT MIN(issued_at)  FROM refresh_tokens f WHERE f.family_id = r.family_id)
		   FROM refresh_tokens r
		  WHERE r.used_at IS NULL AND (? = '' OR r.sub = ?)
		  ORDER BY r.issued_at DESC
		  LIMIT ?`, sub, sub, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resources.SessionsRow{}
	for rows.Next() {
		var (
			r         resources.SessionsRow
			revokedAt sql.NullString
		)
		if err := rows.Scan(&r.FamilyID, &r.Sub, &r.Scope, &r.Aud, &r.RenewedAt,
			&r.ExpiresAt, &revokedAt, &r.Rotations, &r.StartedAt); err != nil {
			return nil, err
		}
		r.Status = sessionStatus(revokedAt.Valid, r.ExpiresAt, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// sessionStatus collapses the tombstone columns plus the clock into one word.
// Revoked outranks expired: how a session ended is what an incident review
// asks, and a family killed a day before its natural expiry must not be
// reported as having merely timed out.
func sessionStatus(revoked bool, expiresAt string, at time.Time) string {
	if revoked {
		return "revoked"
	}
	if exp, err := time.Parse(time.RFC3339, expiresAt); err == nil && !at.Before(exp) {
		return "expired"
	}
	return "active"
}

package main

// sessions_resource.go publishes refresh-token FAMILIES at GET /v1/sessions and
// kills one at DELETE /v1/sessions/{family_id}.
//
// It closes the other two of spec 5/1's open items. Before it, `FROM
// refresh_tokens` had exactly ONE reader in the tree —
// authstore.RefreshTokenBySecret, `WHERE token_hash = ?` — so there was no way
// to ask who was logged in; and the family revoke fired only from reuse
// detection and from the user's own
// `/auth/logout` cookie, so an operator holding a compromised account could not
// cut the session off. Self-service logout was never the gap; admin-initiated
// revocation was (BUGS F15).
//
// WHERE THE AUDIT ROW LANDS, which BUGS F15a called out as undecided: auth.db,
// written by resreg.invoke, and readable because 5/I federated authd's
// audit_log into /dash/audit/. The objection at the time — "an
// operator-initiated revoke recorded only in auth.db is invisible on the page
// that exists to show it" — was true when it was written and is not true now,
// which is what unblocks this endpoint rather than any argument about it.
//
// Taking resreg's standard `delete` action is what buys that row without a
// bespoke emit. What it no longer buys is one transaction around the revoke and
// the row: revokeSession below says why, and what closes it.

import (
	"context"
	"net/http"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// sessionsResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/sessions.go).
func (s *server) sessionsResource() resreg.Resource {
	r := resreg.Mount(resources.SessionsName)
	r.Authz = func(_ resreg.Caller, _ resreg.Action, args resreg.Args) (string, map[string]string, error) {
		return "", map[string]string{"family_id": args.Str("family_id")}, nil
	}
	r.Gate = s.sessionsGate
	r.Handler = s.sessionsHandler
	r.Store = s.auditStore()
	return r
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
		rows, err := s.a.st.SessionsByFamily(x.Args.Str("sub"), int(x.Args.Int("limit")), time.Now())
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

// revokeSession kills one family through the store, and its audit row is
// written by resreg into the transaction it opened on auth.db.
//
// The two are NO LONGER one transaction. The revoke is a guarded batch on the
// refresh family; the audit row is still a statement audit runs against the raw
// *sql.DB (BUGS L251), and a batch cannot join a transaction it does not own.
// So an audit write that failed after this returned would leave the family
// revoked, where it used to roll the revoke back. Both halves become one
// GuardedBatch when audit_log is an entity, which is what spec 6/40 §"The
// audited write commits in a GuardedBatch" is for; until then this is the one
// guarantee the port gives up, and it is stated rather than hidden.
//
// A family_id that matches nothing is a 404 rather than a silent success: an
// operator who mistypes an id during an incident must not be told the session
// is dead. The `revoked_at` Absent guard means re-revoking an already-dead
// family also 404s, which is the same honest answer — nothing was killed here.
func (s *server) revokeSession(x resreg.Execution) (any, error) {
	fam := x.Args.Str("family_id")
	if fam == "" {
		return nil, resreg.Errorf(http.StatusBadRequest, "family_id required")
	}
	killed, err := s.a.st.RevokeFamily(fam)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !killed {
		return nil, resreg.Errorf(http.StatusNotFound, "no live session %q", fam)
	}
	return map[string]any{"family_id": fam, "revoked": 1}, nil
}

package main

// sessions_resource.go publishes refresh-token FAMILIES at GET /v1/sessions and
// kills one at DELETE /v1/sessions/{family_id}: admin-initiated revocation
// (BUGS F15). The audit row lands in authd's audit_log, written by
// resreg.invoke, and is readable because 5/I-tool-call-logging federates it into /dash/audit/.

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

// sessionsGate splits read from write: listing who is logged in and ending
// someone's session are different authorities.
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

// revokeSession kills one family through the store. The revoke is the store's
// own guarded batch on the family and the audit row is resreg's, sent after
// this returns, so an audit write that fails leaves the family revoked.
//
// A family_id that matches nothing is a 404, not a silent success: an operator
// who mistypes an id during an incident must not be told the session is dead.
// Re-revoking an already-dead family also 404s.
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

package routd

// pending_actions_http.go mounts the REST half of spec 5/19's resolution
// contract — GET /v1/pending_actions plus POST /v1/pending_actions/{id}/
// approve|reject — closing the "declared but not mounted" gap BUGS F66
// records. Both verdicts funnel through resolveHoldTx, the same core the chat
// /approve command runs, inside the tx resreg opens: verdict, resolution
// message and audit row commit as one unit.
//
// There is deliberately no Enqueue here: resreg has no post-commit hook, and
// an enqueue before commit can race the dispatch ahead of the approved row.
// The loop's poll (pollEvery, 2s default) picks the committed resolution
// message up — the same backstop every externally-written message row rides.
//
// The agent-socket MCP face stays unmounted: approve on the held agent's own
// socket would let it approve its own call, so that half of F66 waits for an
// operator-socket design rather than shipping as a hole.

import (
	"context"
	"errors"
	"net/http"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

const (
	actionApprove = resreg.Action("approve")
	actionReject  = resreg.Action("reject")
)

// pendingActionsResource is the mounted face of the catalog registration in
// resreg/resources/pending_actions.go. Store is non-nil so resreg runs the
// Gate and opens the verdict tx.
func (s *Server) pendingActionsResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.PendingActionsName,
		Endpoints: resources.PendingActionsEndpoints,
		MCPDoc:    resources.PendingActionsMCPDoc,
		MCPArgs:   resources.PendingActionsMCPArgs,
		MCPNames:  resources.PendingActionsMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.pendingActionsHandler,
		Store:   store.New(s.db.SQL()),
	}
}

func (s *Server) mountPendingActions(mux *http.ServeMux) {
	res := s.pendingActionsResource()
	res.Gate = s.pendingActionsRESTGate
	// Same verified-bearer Caller builder the audit face uses: sub + scopes +
	// folder claim off the token, open when no Verifier (local dev).
	resreg.RegisterREST(mux, res, s.auditRESTCaller)
}

// pendingActionsRESTGate requires pending_actions:read for the list and
// pending_actions:write for a verdict. Like audit:read, both scopes are
// unreachable by any human bearer (a user token's scope list holds folder
// globs); the sole holder is service:dashd, whose /dash/approvals/ page is
// requireOperator-gated — that gate is the whole containment, matching the
// chat path's IsOperator.
func (s *Server) pendingActionsRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil // local-dev open mode, mirrors s.authz
	}
	verb := "read"
	if x.Action.Mutates() {
		verb = "write"
	}
	if !auth.HasScope(x.Caller.Scopes(), "pending_actions", verb) {
		return resreg.Errorf(http.StatusForbidden, "missing scope pending_actions:%s", verb)
	}
	return nil
}

func (s *Server) pendingActionsHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		folder := x.Args.Str("folder")
		// A folder-claimed caller is pinned to its own folder; the argument
		// cannot widen it. An empty claim (service:dashd) lists all — safe only
		// behind the operator gate, same reading as the audit face.
		if x.Caller.Folder != "" {
			folder = x.Caller.Folder
		}
		rows, err := s.db.ListPendingActions(folder, x.Args.Str("status"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"pending": rows}, nil
	case actionApprove, actionReject:
		id := x.Args.Str("id")
		if id == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "id required")
		}
		verdict := PendingApproved
		if x.Action == actionReject {
			verdict = PendingRejected
		}
		// reviewed_by records the OPERATOR, not the transport: dashd calls with
		// its service bearer and forwards the proxyd-verified operator sub as
		// `reviewer`. Trusting the argument is bounded by the scope gate — the
		// only pending_actions:write holder is service:dashd, whose page is
		// operator-gated. Absent (curl, local dev), the bearer's sub stands.
		reviewer := x.Args.Str("reviewer")
		if reviewer == "" {
			reviewer = x.Caller.Sub
		}
		p, _, err := resolveHoldTx(x.Tx, id, verdict, reviewer, x.Args.Str("note"))
		switch {
		case errors.Is(err, ErrPendingNotFound):
			return nil, resreg.Errorf(http.StatusNotFound, "%v", err)
		case errors.Is(err, ErrPendingResolved):
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		case err != nil:
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return p, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

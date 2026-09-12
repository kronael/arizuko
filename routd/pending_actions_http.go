package routd

// pending_actions_http.go mounts the REST half of spec 5/19's resolution
// contract — GET /v1/pending_actions plus POST /v1/pending_actions/{id}/
// approve|reject — closing the "declared but not mounted" gap BUGS F66
// records. Both verdicts funnel through resolveHoldTx, the same core the chat
// /approve command runs, inside the tx resreg opens: verdict, standing rule and
// audit row commit as one unit.
//
// Nothing is dispatched from here. The agent is suspended on the held call, so
// the verdict reaching the row IS the delivery: the waiter reads it within a
// poll and runs or refuses the call inside the turn that asked.
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
)

const (
	actionApprove = resreg.Action("approve")
	actionReject  = resreg.Action("reject")
)

// pendingActionsResource is the mounted face of the catalog registration in
// resreg/resources/pending_actions.go. Store is non-nil so resreg runs the
// Gate and opens the verdict tx.
func (s *Server) pendingActionsResource() resreg.Resource {
	r := resreg.Mount(resources.PendingActionsName)
	r.Handler = s.pendingActionsHandler
	r.Store = s.db.store()
	return r
}

func (s *Server) mountPendingActions(mux *http.ServeMux) {
	res := s.pendingActionsResource()
	res.Gate = s.pendingActionsRESTGate
	// Same verified-bearer Caller builder the audit face uses: sub + scopes +
	// folder claim off the token, open when no Verifier (local dev).
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// pendingActionsRESTGate requires pending_actions:read for the list and
// pending_actions:write for a verdict. Like audit:read, both scopes are
// unreachable by any human bearer (a user token's scope list holds folder
// globs); the sole holder is service:dashd, which forwards the proxyd-verified
// human as `reviewer`. This gate decides only that the TRANSPORT may speak for
// a human; which folders that human may read and answer is resolveHoldTx's one
// admin gate, applied per row.
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

func (s *Server) pendingActionsHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		folder := x.Args.Str("folder")
		// A folder-claimed caller is pinned to its own folder; the argument
		// cannot widen it. An empty claim (service:dashd) reads every folder,
		// and `reviewer` is what narrows it back down.
		if x.Caller.Folder != "" {
			folder = x.Caller.Folder
		}
		rows, err := s.db.ListPendingActions(folder, x.Args.Str("status"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"pending": s.reviewable(x.Args.Str("reviewer"), rows)}, nil
	case actionApprove, actionReject:
		id := x.Args.Str("id")
		if id == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "id required")
		}
		verdict := PendingApproved
		if x.Action == actionReject {
			verdict = PendingRejected
		}
		// reviewed_by records the HUMAN, not the transport: dashd calls with its
		// service bearer and forwards the proxyd-verified sub as `reviewer`.
		// Trusting the argument is bounded by the scope gate — the only
		// pending_actions:write holder is service:dashd — and by resolveHoldTx,
		// which authorizes this exact string on the held row's folder, so a
		// forwarded sub buys no authority the human does not hold. Absent (curl,
		// local dev), the bearer's sub stands.
		reviewer := x.Args.Str("reviewer")
		if reviewer == "" {
			reviewer = x.Caller.Sub
		}
		// `standing` is the `always` verdict — approve plus the skip-approval
		// rule, in this tx. It is an ARGUMENT of approve rather than a third
		// action because pending_actions has no third status: the standing
		// answer lives in acl (spec 6/26 § Decision 5). Meaningless on reject,
		// and there is no standing reject — see resolveHoldTx.
		standing := x.Action == actionApprove && x.Args.Bool("standing")
		p, err := s.db.resolveHoldTx(ctx, x.Tx, id, verdict, reviewer, x.Args.Str("note"), standing)
		switch {
		case errors.Is(err, ErrPendingNotFound):
			return nil, resreg.Errorf(http.StatusNotFound, "%v", err)
		case errors.Is(err, ErrPendingResolved):
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		case errors.Is(err, ErrPendingForbidden):
			return nil, resreg.Errorf(http.StatusForbidden, "%v", err)
		case err != nil:
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return p, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// reviewable keeps the rows `reviewer` may actually answer — the SAME
// admin-on-the-held-folder question resolveHoldTx asks, so the queue a human
// reads and the verdicts that human can give are one decision rather than two.
// Listing a folder the reader cannot act on is an invitation to a 403.
//
// An empty reviewer is the unfiltered read (curl with the service bearer, local
// dev): the scope gate above is the whole containment there, unchanged.
func (s *Server) reviewable(reviewer string, rows []PendingAction) []PendingAction {
	if reviewer == "" {
		return rows
	}
	out := make([]PendingAction, 0, len(rows))
	for _, p := range rows {
		if s.db.Authorize(reviewer, p.GroupFolder, "admin", nil) {
			out = append(out, p)
		}
	}
	return out
}

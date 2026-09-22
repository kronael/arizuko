package routd

// pending_actions_http.go mounts the REST half of spec 5/19's resolution
// contract — GET /v1/pending_actions plus POST /v1/pending_actions/{id}/
// approve|reject. Both verdicts funnel through resolveHoldInto, the same core
// the chat /approve command runs, inside the tx resreg opens. Nothing is
// dispatched from here: the agent is suspended on the held call, so the
// verdict reaching the row IS the delivery. The agent-socket MCP face stays
// unmounted: approve on the held agent's own socket would let it approve its
// own call (BUGS F66).

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

// pendingActionsResource is the mounted face of the catalog registration.
// Store is non-nil so resreg runs the Gate and opens the verdict tx.
func (s *Server) pendingActionsResource() resreg.Resource {
	r := resreg.Mount(resources.PendingActionsName)
	r.Handler = s.pendingActionsHandler
	r.Store = s.db.store()
	return r
}

func (s *Server) mountPendingActions(mux *http.ServeMux) {
	res := s.pendingActionsResource()
	res.Gate = s.pendingActionsRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// pendingActionsRESTGate requires pending_actions:read for the list and :write
// for a verdict; both are unreachable by a human bearer (a user token's scope
// list holds folder globs), so the sole holder is service:dashd, which
// forwards the proxyd-verified human as `reviewer`. This gate decides only
// that the TRANSPORT may speak for a human; which folders that human may
// answer is resolveHoldInto's admin gate, per row.
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
		// A folder-claimed caller is pinned to its folder; an empty claim
		// (service:dashd) reads every folder, and `reviewer` narrows it back.
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
		// reviewed_by records the HUMAN, not the transport: dashd forwards the
		// proxyd-verified sub as `reviewer`. Trusting it is bounded by the scope
		// gate (only service:dashd holds :write) and by resolveHoldInto, which
		// authorizes this exact string on the held row's folder. Absent (curl,
		// local dev), the bearer's sub stands.
		reviewer := x.Args.Str("reviewer")
		if reviewer == "" {
			reviewer = x.Caller.Sub
		}
		// `standing` is the `always` verdict, an ARGUMENT of approve rather than
		// a third action: pending_actions has no third status (spec 6/26
		// § Decision 5). There is no standing reject — see resolveHoldInto.
		standing := x.Action == actionApprove && x.Args.Bool("standing")
		// A verdict another reviewer landed first refuses the write; the 409
		// travels with it.
		lost := resreg.Errorf(http.StatusConflict,
			"pending action %q %v (by someone else)", id, ErrPendingResolved)
		p, err := s.db.resolveHoldInto(ctx, x.Writes, id, verdict, reviewer,
			x.Args.Str("note"), standing, lost)
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

// reviewable keeps the rows `reviewer` may answer — the SAME
// admin-on-the-held-folder question resolveHoldInto asks, so the queue a human
// reads and the verdicts they can give are one decision, not two. An empty
// reviewer is the unfiltered read (the service bearer, local dev).
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

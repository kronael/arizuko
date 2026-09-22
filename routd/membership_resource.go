package routd

// Unpair (spec 5/31-identity-pairing): acl_membership gains exactly ONE action — delete —
// scoped to added_by='pairing' so it cannot reach role membership. Either
// endpoint of the edge may call it, each face proving it IS that endpoint:
// the AGENT is authorized against the folder the child JID routes to, the
// REST caller's JWT sub must BE the parent, with no operator override. Both
// directions are de-escalation; adding an edge needs the account owner's
// consent at the browser step.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// membershipResource is the single renderer for both unpair faces. contain
// receives the resolved target — the child's routing folder for the agent,
// the row's parent for REST.
func (s *Server) membershipResource(contain containFn) resreg.Resource {
	r := resreg.Mount(resources.ACLMembershipName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.membershipHandler(ctx, x, contain)
	}
	return r
}

func (s *Server) membershipHandler(ctx context.Context, x resreg.Execution, contain containFn) (any, error) {
	if x.Action != resreg.ActionDelete {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	child := strings.TrimSpace(argString(x.Args, "child"))
	parent := strings.TrimSpace(argString(x.Args, "parent"))
	if child == "" || parent == "" {
		return nil, resreg.Errorf(http.StatusBadRequest, "child and parent required")
	}
	target := parent
	if x.Surface != audit.SurfaceREST {
		// Agent face: the caller proves it handles the chat, not that it owns
		// the account — the same read the mint gate does.
		var err error
		if target, err = pairingTargetFolder(s.db, child); err != nil {
			return nil, err
		}
	}
	if err := contain(x.Caller, x.Action, target); err != nil {
		return nil, err
	}
	// added_by='pairing' is the whole scope: a role or manifest edge matches
	// zero rows. The delete conditions on the edge being pairing's, so its
	// refusal IS the not-found answer, reached from the batch.
	if err := s.db.store().UnpairInto(x.Writes, child, parent,
		resreg.Errorf(http.StatusNotFound,
			"no pairing between %s and %s to remove", child, parent)); err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	slog.Info("unpair", "child", child, "parent", parent, "surface", x.Surface)
	return map[string]any{"removed": true, "child": child, "parent": parent}, nil
}

// membershipPostBuild mounts `unpair` on the agent socket; contain authorizes
// the caller against the folder the child JID routes to.
func (s *Server) membershipPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		name := resources.ACLMembershipMCPNames[a]
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	res := s.membershipResource(contain)
	res.Gate = agentAllowGate
	return mountAgentResource(res, callerSub, folder, visible)
}

// mountMembership wires DELETE /v1/acl_membership onto the SAME handler; the
// contain seam is the account-side proof: the caller's verified sub must be
// the edge's parent, so a signed-in human drops only their own pairings.
func (s *Server) mountMembership(mux *http.ServeMux) {
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if s.verify == nil {
			return nil
		}
		// c.Sub carries the JWT "user:" prefix; the stored parent is bare (5/1's
		// sub prefix rule). Comparing them raw never matches.
		if c.Sub == "" || auth.BareSub(c.Sub) != target {
			return resreg.Errorf(http.StatusForbidden,
				"only the linked account may unpair it")
		}
		return nil
	}
	res := s.membershipResource(contain)
	// No scope gate: being the edge's parent IS the authorization, and that
	// check needs the row's parent arg, so it rides contain. resreg's
	// defaultGate would demand an acl_membership:delete scope nobody holds.
	res.Gate = agentAllowGate
	resreg.RegisterREST(mux, res, s.membershipRESTCaller)
}

// membershipRESTCaller carries the verified sub; Folder stays empty because
// the REST face is bound to the account, not a folder subtree. A nil Verifier
// is open.
func (s *Server) membershipRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub string
	if s.verify != nil {
		var err error
		sub, _, _, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{Sub: sub}, nil
}

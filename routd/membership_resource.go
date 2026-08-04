package routd

// Unpair (spec 5/31 § Unpair). acl_membership already existed as a resreg
// resource but declared no Endpoints and no MCPNames, so an edge was removable
// only through `arizuko apply`. It gains exactly ONE action — delete — scoped
// to added_by='pairing' so it cannot reach role membership.
//
// Either endpoint of the edge may call it, and each face proves it IS that
// endpoint through its own injected seam:
//
//   - AGENT (MCP `unpair`): the channel side. contain authorizes the caller
//     against the folder the child JID routes to — the same route-ownership rule
//     the mint and outbound `send` apply.
//   - OPERATOR REST (DELETE /v1/acl_membership): the account side. The caller's
//     JWT sub must BE the parent. There is no operator override: an operator
//     already reaches the row through manifests, and a third path would drift.
//
// Both directions are de-escalation — there is no inverse verb here, because
// adding an edge requires the account owner's consent at the browser step.

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
	"github.com/kronael/arizuko/store"
)

// membershipResource is the single renderer for both unpair faces. contain is
// the per-face endpoint proof (see header); it receives the resolved target —
// the child's routing folder for the agent, the row's parent for REST.
func (s *Server) membershipResource(contain containFn) resreg.Resource {
	r := resreg.Resource{
		Name:      "acl_membership",
		Endpoints: resources.ACLMembershipEndpoints,
		MCPDoc:    resources.ACLMembershipMCPDoc,
		MCPArgs:   resources.ACLMembershipMCPArgs,
		MCPNames:  resources.ACLMembershipMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Store: store.New(s.db.SQL()),
	}
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
		// Agent face: the caller proves it handles the chat, not that it owns the
		// account. Resolving the child's routing folder is the same read the mint
		// gate does, so the two faces of a pairing are bounded identically.
		var err error
		if target, err = pairingTargetFolder(s.db, child); err != nil {
			return nil, err
		}
	}
	if err := contain(x.Caller, x.Action, target); err != nil {
		return nil, err
	}
	// added_by='pairing' is the whole scope: a role membership row (or any
	// manifest-applied edge) matches zero rows and is reported as not-found.
	removed, err := store.UnpairTx(ctx, x.Tx, child, parent)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !removed {
		return nil, resreg.Errorf(http.StatusNotFound,
			"no pairing between %s and %s to remove", child, parent)
	}
	slog.Info("unpair", "child", child, "parent", parent, "surface", x.Surface)
	return map[string]any{"removed": true, "child": child, "parent": parent}, nil
}

// membershipPostBuild mounts `unpair` on the agent socket. contain authorizes
// the caller against the folder the child JID routes to — one evaluator,
// magnitude and containment in one call.
func (s *Server) membershipPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		name := resources.ACLMembershipMCPNames[a]
		if !authorize(callerSub, target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	res := s.membershipResource(contain)
	res.Gate = agentAllowGate
	return mountAgentResource(res, callerSub, folder, visible)
}

// mountMembership wires DELETE /v1/acl_membership onto the SAME handler. The
// contain seam here is the account-side proof: the caller's verified sub must be
// the edge's parent, so a signed-in human can drop their own pairings and
// nobody else's.
func (s *Server) mountMembership(mux *http.ServeMux) {
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if s.verify == nil {
			return nil
		}
		// c.Sub arrives with the JWT "user:" prefix; target is the stored
		// parent, which is bare (5/1's sub prefix rule). Comparing them raw
		// never matches — the same mismatch that made pairing itself inert.
		if c.Sub == "" || auth.BareSub(c.Sub) != target {
			return resreg.Errorf(http.StatusForbidden,
				"only the linked account may unpair it")
		}
		return nil
	}
	res := s.membershipResource(contain)
	// No scope gate: the caller is an ordinary signed-in human dropping their own
	// pairing, not an operator. Being the edge's parent IS the authorization, and
	// that check needs the row's parent arg, so it rides contain in the handler.
	// resreg's operator defaultGate would demand an acl_membership:delete scope
	// nobody holds.
	res.Gate = agentAllowGate
	resreg.RegisterREST(mux, res, s.membershipRESTCaller)
}

// membershipRESTCaller carries the verified sub; Folder stays empty because the
// REST face is bound to the account, not to a folder subtree. A nil Verifier is
// open (local dev), mirroring the other REST callers.
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

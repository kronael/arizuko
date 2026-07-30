package routd

// GET /v1/groups shares groupsHandler with MCP. Creation remains dashd's
// FS-managed SetupGroup, so this surface is intentionally read-only. Because
// groups is a forwarder, Authz checks scope and the handler filters the result
// to the caller's JWT subtree.

import (
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// mountGroups wires only the shared read endpoint.
func (s *Server) mountGroups(mux *http.ServeMux) {
	res := s.groupsResource(s.groupsRESTAuthz)
	res.Endpoints = []resreg.Endpoint{
		{Verb: "GET", Path: "/v1/groups", Action: resreg.ActionList},
	}
	resreg.RegisterREST(mux, res, s.groupsRESTCaller)
}

// groupsRESTCaller sets the JWT subtree filtered by the handler. An empty folder
// means an unrestricted root/service caller; a nil Verifier is open.
func (s *Server) groupsRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, jwtFolder string
	var scope []string
	if s.verify != nil {
		var err error
		sub, scope, jwtFolder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{
		Sub:    sub,
		Folder: jwtFolder,
		Claims: map[string]string{"scopes": strings.Join(scope, " ")},
	}, nil
}

// groupsRESTAuthz checks read scope; the handler enforces result containment.
func (s *Server) groupsRESTAuthz(c resreg.Caller, _ resreg.Action, _ resreg.Args) (string, map[string]string, error) {
	if s.verify == nil {
		return "", nil, nil
	}
	if !hasAnyScope(strings.Fields(c.Claims["scopes"]), []string{"routes:read", "routes:read:own_group", "acl:read"}) {
		return "", nil, resreg.Errorf(http.StatusForbidden, "missing read scope")
	}
	return "", nil, nil
}

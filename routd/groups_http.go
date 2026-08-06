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

// mountGroups mounts resources.GroupsAgentEndpoints verbatim: register is MCPOnly
// there, so RegisterREST and openapi.go both skip it and only the shared read
// endpoint is served. NEVER reintroduce an inline Endpoints literal here — the
// trimmed copy that used to live in this function is why `groups` had to be held
// out of OpenAPIResources (BUGS F27); endpoints_source_test.go probes this mux.
func (s *Server) mountGroups(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.groupsResource(s.groupsRESTAuthz), s.groupsRESTCaller)
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

package routd

// GET /v1/groups shares groupsHandler with MCP. Creation remains dashd's
// FS-managed SetupGroup, so this surface is intentionally read-only. Because
// groups is a forwarder, Authz checks scope and the handler filters the result
// to the caller's JWT subtree.

import (
	"net/http"

	"github.com/kronael/arizuko/resreg"
)

// mountGroups mounts resources.GroupsAgentEndpoints verbatim: register is
// MCPOnly, so only the shared read endpoint is served. NEVER an inline
// Endpoints literal here — a trimmed copy held `groups` out of
// OpenAPIResources (BUGS F27); endpoints_source_test.go probes this mux.
func (s *Server) mountGroups(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.groupsResource(s.groupsRESTAuthz), s.restCaller(nil))
}

// groupsRESTAuthz checks read scope; the handler enforces result containment.
func (s *Server) groupsRESTAuthz(c resreg.Caller, _ resreg.Action, _ resreg.Args) (string, map[string]string, error) {
	if s.verify == nil {
		return "", nil, nil
	}
	if !hasAnyScope(c.Scopes(), []string{"routes:read", "routes:read:own_group", "acl:read"}) {
		return "", nil, resreg.Errorf(http.StatusForbidden, "missing read scope")
	}
	return "", nil, nil
}

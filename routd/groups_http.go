package routd

import (
	"net/http"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
)

// mountGroups mounts resources.GroupsAgentEndpoints verbatim: register is
// MCPOnly, so only the shared read endpoints are served. NEVER an inline
// Endpoints literal here — a trimmed copy held `groups` out of
// OpenAPIResources (BUGS F27); endpoints_source_test.go probes this mux.
func (s *Server) mountGroups(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.groupsResource(s.groupsRESTAuthz), s.restCaller(nil))
}

// groupsRESTAuthz binds each operation to its scope and target.
func (s *Server) groupsRESTAuthz(c resreg.Caller, action resreg.Action, args resreg.Args) (string, map[string]string, error) {
	var target string
	var params map[string]string
	if action == resreg.ActionGet || action == resreg.ActionUpdate {
		target = args.Str("folder")
		if !groupfolder.IsValidFolder(target) {
			return "", nil, resreg.Errorf(http.StatusBadRequest, "invalid folder")
		}
		if !ownsFolder(c.Folder, target) {
			return "", nil, resreg.Errorf(http.StatusForbidden, "forbidden")
		}
		params = map[string]string{"folder": target}
	}
	if s.verify == nil {
		return target, params, nil
	}
	scopes := []string{"routes:read", "routes:read:own_group", "acl:read"}
	if action == resreg.ActionUpdate {
		scopes = []string{"routes:write", "routes:write:own_group"}
	}
	if !hasAnyScope(c.Scopes(), scopes) {
		return "", nil, resreg.Errorf(http.StatusForbidden, "missing read scope")
	}
	return target, params, nil
}

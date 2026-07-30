package routd

// groups_http.go is the read-only operator REST face GET /v1/groups (spec 5/16 +
// 5/17). It lists the folders the caller's JWT subtree contains — the human twin
// of the agent's refresh_groups MCP tool, sharing one groupsHandler. Only the
// LIST endpoint is mounted: operator group CREATION stays dashd's FS-managed
// SetupGroup (skills/settings/tasks seeding — the group-creation write-discipline),
// so routd never opens a second bare create door. groups is a forwarder (Store
// nil), so invoke runs Authz (not Gate); the subtree containment that closes the
// rest_listall leak lives in the handler (surface==REST → ownsFolder filter).

import (
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// mountGroups wires the read-only GET /v1/groups REST surface onto the shared
// groupsHandler via resreg. POST (register) is deliberately omitted — see header.
func (s *Server) mountGroups(mux *http.ServeMux) {
	res := s.groupsResource(s.groupsRESTAuthz)
	res.Endpoints = []resreg.Endpoint{
		{Verb: "GET", Path: "/v1/groups", Action: resreg.ActionList},
	}
	resreg.RegisterREST(mux, res, s.groupsRESTCaller)
}

// groupsRESTCaller resolves the operator identity from the bearer and sets
// Caller.Folder to the JWT folder — the subtree the handler filters the listing
// to. A nil Verifier is open (single-tenant/local-dev, mirroring s.authz): empty
// folder → ownsFolder("", g)==true, so the list spans all (root/service token).
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

// groupsRESTAuthz is the forwarder's REST gate: require a valid bearer carrying a
// read scope. Subtree containment of the RESULT is enforced in the handler (the
// surface==REST filter), so this only checks the caller may read at all. A nil
// Verifier is open.
func (s *Server) groupsRESTAuthz(c resreg.Caller, _ resreg.Action, _ resreg.Args) (string, map[string]string, error) {
	if s.verify == nil {
		return "", nil, nil
	}
	if !hasAnyScope(strings.Fields(c.Claims["scopes"]), []string{"routes:read", "routes:read:own_group", "acl:read"}) {
		return "", nil, resreg.Errorf(http.StatusForbidden, "missing read scope")
	}
	return "", nil, nil
}

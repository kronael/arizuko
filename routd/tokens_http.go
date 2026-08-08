package routd

// Route-token REST operations share routeTokensHandler with MCP and inject
// REST-specific identity, scope, and folder containment. Token DELIVERY
// (URL token -> jid) is not here and never was a live REST call: webd and
// proxyd are FS-mounted on routd.db and resolve in-process (spec 5/W).

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
)

var segRe = regexp.MustCompile(`^[\w-]+$`)

// descendant reports whether target equals owner or nests under it. Used by
// ownsFolder (server.go) for JWT-folder containment.
func descendant(target, owner string) bool {
	return groupfolder.Contains(owner, target)
}

// mountRouteTokens wires the shared route-token CRUD faces. Every path routd
// serves under /v1/route_tokens comes from the resource declaration, so the
// mux and /openapi.json cannot disagree (TestRouteTokens_NoHandRolledResolve).
func (s *Server) mountRouteTokens(mux *http.ServeMux) {
	res := s.routeTokensResource()
	res.Gate = s.routeTokensRESTGate
	resreg.RegisterREST(mux, res, s.routeTokensRESTCaller)
}

// routeTokensRESTCaller sets the token owner as Caller.Folder and passes held
// scopes and the JWT folder to the REST gate. A nil Verifier is open.
func (s *Server) routeTokensRESTCaller(r *http.Request) (resreg.Caller, error) {
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
		Folder: routeTokensOwner(r, jwtFolder),
		Claims: map[string]string{
			"jwt_folder": jwtFolder,
			"scopes":     strings.Join(scope, " "),
		},
	}, nil
}

// routeTokensOwner resolves the owner bound by the shared handler. An explicit
// query/body owner wins; otherwise the JWT folder applies. An empty JWT folder
// means an unrestricted root/service caller. The body is restored for resreg.
func routeTokensOwner(r *http.Request, jwtFolder string) string {
	if r.Method == http.MethodPost {
		if o := peekBodyOwner(r); o != "" {
			return o
		}
		return jwtFolder
	}
	if q := r.URL.Query().Get("owner_folder"); q != "" {
		return q
	}
	return jwtFolder
}

// peekBodyOwner reads r.Body to grab the "owner_folder" field, then restores it
// so resreg.decodeRESTArgs can re-read the body. A nil/malformed body yields "".
func peekBodyOwner(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	buf, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(buf)))
	var body struct {
		OwnerFolder string `json:"owner_folder"`
	}
	_ = json.Unmarshal(buf, &body)
	return body.OwnerFolder
}

// routeTokensRESTGate applies REST scopes and JWT-folder containment. An empty
// JWT folder is unrestricted; mint authorization remains in the shared handler.
func (s *Server) routeTokensRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	held := strings.Fields(x.Caller.Claims["scopes"])
	want := []string{"routes:read", "routes:read:own_group"}
	if x.Action.Mutates() {
		want = []string{"routes:write", "routes:write:own_group"}
	}
	if !hasAnyScope(held, want) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	if !ownsFolder(x.Caller.Claims["jwt_folder"], x.Caller.Folder) {
		return resreg.Errorf(http.StatusForbidden, "owner_folder outside caller subtree: %s", x.Caller.Folder)
	}
	return nil
}

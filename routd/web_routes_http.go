package routd

// Web-route REST operations share webRoutesHandler with MCP and inject
// REST-specific identity, scope, and folder containment. Owner lookup and
// presence remain hand-rolled endpoints.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// mountWebRoutes wires shared web-route CRUD and the hand-rolled readers.
func (s *Server) mountWebRoutes(mux *http.ServeMux) {
	res := s.webRoutesResource()
	res.Gate = s.webRoutesRESTGate
	resreg.RegisterREST(mux, res, s.webRoutesRESTCaller)
	mux.HandleFunc("GET /v1/web_routes/owner", s.handleWebRouteOwner)
	mux.HandleFunc("GET /v1/web_presence", s.handleWebPresence)
}

// webRoutesRESTCaller sets the route target as Caller.Folder and passes held
// scopes and the JWT folder to the REST gate. A nil Verifier is open.
func (s *Server) webRoutesRESTCaller(r *http.Request) (resreg.Caller, error) {
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
		Folder: webRoutesTarget(r, jwtFolder),
		Claims: map[string]string{
			"jwt_folder": jwtFolder,
			"scopes":     strings.Join(scope, " "),
		},
	}, nil
}

// webRoutesTarget resolves the folder acted on by the shared handler. An
// explicit query/body folder wins; otherwise the JWT folder applies. An empty
// JWT folder means an unrestricted root/service caller. The body is restored
// for resreg.
func webRoutesTarget(r *http.Request, jwtFolder string) string {
	if r.Method == http.MethodGet {
		if q := r.URL.Query().Get("folder"); q != "" {
			return q
		}
		return jwtFolder
	}
	if f := peekBodyFolder(r); f != "" {
		return f
	}
	return jwtFolder
}

// peekBodyFolder reads r.Body to grab the "folder" field, then restores it so
// resreg.decodeRESTArgs can re-read the body (resreg builds the Caller before
// decoding Args). A nil/malformed body yields "".
func peekBodyFolder(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	buf, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	var body struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal(buf, &body)
	return body.Folder
}

// webRoutesRESTGate applies REST scopes and JWT-folder containment. An empty
// JWT folder is unrestricted.
func (s *Server) webRoutesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
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
		return resreg.Errorf(http.StatusForbidden, "folder outside caller subtree: %s", x.Caller.Folder)
	}
	return nil
}

// handleWebRouteOwner reports the folder owning an exact path prefix.
func (s *Server) handleWebRouteOwner(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "routes:read", "routes:read:own_group") {
		return
	}
	owner, _ := s.db.WebRouteOwner(r.URL.Query().Get("path_prefix"))
	writeJSON(w, 200, map[string]string{"owner": owner})
}

// handleWebPresence is the REST twin of the get_web_presence MCP tool: it
// reports a folder's derived/aliased canonical host + always-works /pub path
// (spec 5/V). Same renderer (s.webPresence), same folder containment as the
// MCP tool — a scoped caller may only query its own subtree.
func (s *Server) handleWebPresence(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	q := r.URL.Query().Get("folder")
	if q == "" {
		q = folder
	}
	if q == "" {
		writeErr(w, 400, "missing_field", "folder required")
		return
	}
	if folder != "" && !ownsFolder(folder, q) {
		writeErr(w, 403, "forbidden", "folder outside caller subtree: "+q)
		return
	}
	writeJSON(w, 200, s.webPresence(q))
}

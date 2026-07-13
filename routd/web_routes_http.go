package routd

// web_routes_http.go is the /v1/web_routes REST face. Its list/create/delete
// verbs ride the SAME shared handler (webRoutesHandler, web_routes_resource.go)
// the agent's set_web_route/del_web_route/list_web_routes MCP tools use — the
// spec 5/16 REST-face fold. resreg.RegisterREST mounts them on a copy of
// s.webRoutesResource() with a REST-specific Caller + Gate injected, so ALL the
// REST-only policy (scope + JWT-folder containment, target resolution) lives
// here and the shared handler stays surface-agnostic (it only ever reads
// x.Caller.Folder). The owner-lookup + web-presence twins stay hand-rolled: the
// first has no MCP twin (it's a set_web_route pre-check), the second's MCP twin
// is get_web_presence via buildGatedFns.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// mountWebRoutes wires the /v1/web_routes REST surface. list/create/delete share
// webRoutesHandler through resreg with the operator/human REST Gate + Caller
// injected; the owner-lookup + presence twins stay hand-rolled.
func (s *Server) mountWebRoutes(mux *http.ServeMux) {
	res := s.webRoutesResource()
	res.Gate = s.webRoutesRESTGate
	resreg.RegisterREST(mux, res, s.webRoutesRESTCaller)
	mux.HandleFunc("GET /v1/web_routes/owner", s.handleWebRouteOwner)
	mux.HandleFunc("GET /v1/web_presence", s.handleWebPresence)
}

// webRoutesRESTCaller builds the REST Caller for the shared web_routes handler.
// It resolves identity via the Verifier, then sets Caller.Folder to the TARGET
// the handler acts on (webRoutesTarget). The held scopes + JWT folder ride in
// Claims for webRoutesRESTGate. A nil Verifier is open (single-tenant/local-dev,
// mirroring s.authz): empty principal + empty JWT folder (tier 0 → list spans
// all).
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

// webRoutesTarget resolves the folder the shared handler acts on. GET reads
// ?folder= (empty → "" ONLY for a root/service token with no folder claim so the
// list spans every folder, else the caller's own JWT folder — the old
// handleWebRoutesList scoping, which keys on folder=="" NOT tier: a scoped tenant
// at a top-level folder is tier-0 yet must still see only its own routes).
// PUT/DELETE take the body's folder (empty → the JWT folder; the create self-slot
// + delete scoping need a real folder, never ""). resreg builds the Caller before
// decoding Args, so the body is read then restored (peekBodyFolder) for resreg's
// own decode.
func webRoutesTarget(r *http.Request, jwtFolder string) string {
	if r.Method == http.MethodGet {
		if q := r.URL.Query().Get("folder"); q != "" {
			return q
		}
		if jwtFolder == "" {
			return "" // root/service token (no folder claim) is unrestricted
		}
		return jwtFolder // a folder-scoped token (any tier) lists only its own
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

// webRoutesRESTGate is the operator/human REST twin of the agent socket's tier
// Gate: an any-of scope check on the caller's held scopes, then JWT-folder
// containment over the target (Caller.Folder). Same shared handler, a different
// injected gate (CLAUDE.md "auth is a uniform middleware"). It reproduces the
// retired hand-rolled REST auth verbatim — routes:read/write [+:own_group] via
// hasAnyScope, then ownsFolder. A tier-0 list sets Caller.Folder="" (list all)
// and ownsFolder(x,"") is true, so containment is a no-op there, matching the
// old empty-folder path. A nil Verifier is open (mirrors s.authz).
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

// handleWebRouteOwner is the first-claim owner lookup, relocated off GET
// /v1/web_routes (which the resreg list now owns) to its own path so ServeMux can
// route it. It reports the folder owning an exact ?path_prefix= (or "" when
// unclaimed) — the set_web_route pre-check. Same routes:read auth as the list.
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

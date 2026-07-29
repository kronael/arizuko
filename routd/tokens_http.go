package routd

// tokens_http.go holds the route_token REST face. Issue (/chat, /hook), list,
// and revoke now ride the SAME shared handler (routeTokensHandler,
// route_tokens_resource.go) the agent's issue_chat_link/issue_webhook/
// list_tokens/revoke_token MCP tools use — the spec 5/16 REST-face fold.
// resreg.RegisterREST mounts them on a copy of s.routeTokensResource() with a
// REST-specific Caller + Gate injected, so ALL the REST-only policy (routes
// scope + JWT-folder containment) lives here and the shared handler stays
// surface-agnostic (it reads only x.Caller.Folder = the owner). The
// service-token resolve (URL token → jid) webd calls has NO agent MCP twin, so
// it stays hand-rolled below. DB methods live in tokens.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/resreg"
)

var segRe = regexp.MustCompile(`^[\w-]+$`)

// descendant reports whether target equals owner or nests under it. Used by
// ownsFolder (server.go) for JWT-folder containment.
func descendant(target, owner string) bool {
	return target == owner || strings.HasPrefix(target, owner+"/")
}

// mountRouteTokens wires the /v1/route_tokens REST surface. chat/hook/list/revoke
// share routeTokensHandler through resreg with the operator/human REST Gate +
// Caller injected; the service-token resolve stays hand-rolled (no MCP twin).
func (s *Server) mountRouteTokens(mux *http.ServeMux) {
	res := s.routeTokensResource()
	res.Gate = s.routeTokensRESTGate
	resreg.RegisterREST(mux, res, s.routeTokensRESTCaller)
	mux.HandleFunc("POST /v1/route_tokens/resolve", s.handleTokenResolve)
}

// routeTokensRESTCaller builds the REST Caller for the shared token handler. It
// resolves identity via the Verifier, then sets Caller.Folder to the OWNER the
// handler binds the token to (routeTokensOwner) — client-supplied owner_folder
// for the operator face, defaulting to the JWT folder. The held scopes + JWT
// folder ride in Claims for routeTokensRESTGate. A nil Verifier is open
// (single-tenant/local-dev, mirroring s.authz): empty principal + empty JWT
// folder (tier 0 → list spans all).
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

// routeTokensOwner resolves the owner_folder the shared handler binds the token
// to. GET/DELETE read ?owner_folder= (empty → "" ONLY for a root/service token
// with no folder claim so the list spans every folder, else the JWT folder —
// the old handleTokenList/Revoke scoping, which keys on folder=="" NOT tier).
// POST reads the body's owner_folder (empty → the JWT folder; issue needs a real
// owner, never ""). resreg builds the Caller before decoding Args, so the body
// is read then restored (peekBodyOwner) for resreg's own decode.
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
	if jwtFolder == "" {
		return "" // root/service token (no folder claim) is unrestricted
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

// routeTokensRESTGate is the operator/human REST twin of the agent socket's tier
// Gate: an any-of scope check on the caller's held scopes, then JWT-folder
// containment over the owner (Caller.Folder). Same shared handler, a different
// injected gate (CLAUDE.md "auth is a uniform middleware"). It reproduces the
// retired hand-rolled REST auth — routes:read/write [+:own_group] via
// hasAnyScope, then ownsFolder. A tier-0 list sets Caller.Folder="" (list all)
// and ownsFolder(x,"") is true, so containment is a no-op there. The mint tier
// cap over target_folder stays in the shared handler (authorizeRouteTokenMint).
// A nil Verifier is open (mirrors s.authz).
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

func (s *Server) handleTokenResolve(w http.ResponseWriter, r *http.Request) {
	// webd's service token resolves a URL token → jid.
	if !s.authed(w, r, "routes:read") {
		return
	}
	var req apiv1.ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	jid, owner, context, err := s.db.ResolveRouteToken(req.Token)
	if err != nil {
		writeErr(w, 404, "unknown_token", "token not found")
		return
	}
	writeJSON(w, 200, apiv1.ResolveResponse{JID: jid, OwnerFolder: owner, Context: context})
}

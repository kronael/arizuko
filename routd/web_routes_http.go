package routd

// web_routes_http.go holds the web_route HTTP handlers (list/put/delete) —
// the path-prefix → folder access map served at /v1/web_routes. The DB
// methods live in reads.go/tokens.go; the routes CRUD is in routes_http.go.

import (
	"encoding/json"
	"net/http"
	"strings"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func (s *Server) handleWebRoutesList(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	// ?path_prefix= → first-claim owner lookup for set_web_route (the
	// StoreFns.WebRouteOwner pre-check). Returns the owning folder or "".
	if p := r.URL.Query().Get("path_prefix"); p != "" {
		owner, _ := s.db.WebRouteOwner(p)
		writeJSON(w, 200, map[string]string{"owner": owner})
		return
	}
	q := r.URL.Query().Get("folder")
	if folder != "" { // scoped caller: bind the listing to its own subtree
		if q == "" {
			q = folder
		} else if !ownsFolder(folder, q) {
			writeErr(w, 403, "forbidden", "folder outside caller subtree: "+q)
			return
		}
	}
	rows, err := s.db.WebRoutes(q)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := make([]apiv1.WebRoute, 0, len(rows))
	for _, x := range rows {
		out = append(out, apiv1.WebRoute{PathPrefix: x.PathPrefix, Access: x.Access,
			RedirectTo: x.RedirectTo, Folder: x.Folder, CreatedAt: x.CreatedAt})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleWebRoutePut(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var req apiv1.WebRoute
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, req.Folder) {
		writeErr(w, 403, "forbidden", "folder outside caller subtree: "+req.Folder)
		return
	}
	// Path/access validation + redirect self-slot + first-claim ownership — these
	// MUST mirror the set_web_route MCP twin (ipc/ipc.go, spec 5/V §4): both faces
	// enforce the same containment, else the REST path is a cross-folder web-path
	// hijack + open-redirect (one renderer, many sinks).
	if req.PathPrefix == "" || req.PathPrefix[0] != '/' {
		writeErr(w, 400, "bad_request", "path_prefix must start with /")
		return
	}
	switch req.Access {
	case "public", "auth", "deny", "redirect":
	default:
		writeErr(w, 400, "bad_request", "access must be one of: public, auth, deny, redirect")
		return
	}
	inOwnSlot := strings.HasPrefix(req.PathPrefix, "/pub/"+req.Folder+"/") ||
		strings.HasPrefix(req.PathPrefix, "/priv/"+req.Folder+"/")
	if req.Access == "redirect" {
		if req.RedirectTo == "" {
			writeErr(w, 400, "bad_request", "redirect_to required when access=redirect")
			return
		}
		if !strings.HasPrefix(req.RedirectTo, "/pub/"+req.Folder+"/") &&
			!strings.HasPrefix(req.RedirectTo, "/priv/"+req.Folder+"/") {
			writeErr(w, 403, "forbidden",
				"redirect_to must point into this folder's own slot: /pub/"+req.Folder+"/ or /priv/"+req.Folder+"/")
			return
		}
	}
	if !inOwnSlot {
		if owner, ok := s.db.WebRouteOwner(req.PathPrefix); ok && owner != req.Folder {
			writeErr(w, 403, "forbidden", "path prefix already claimed by folder: "+owner)
			return
		}
	}
	err := s.db.PutWebRoute(WebRouteRow{PathPrefix: req.PathPrefix, Access: req.Access,
		RedirectTo: req.RedirectTo, Folder: req.Folder})
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

func (s *Server) handleWebRouteDelete(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var req apiv1.WebRoute
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, req.Folder) {
		writeErr(w, 403, "forbidden", "folder outside caller subtree: "+req.Folder)
		return
	}
	deleted, err := s.db.DeleteWebRoute(req.PathPrefix, req.Folder)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": deleted})
}

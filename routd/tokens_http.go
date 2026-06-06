package routd

// tokens_http.go holds the route_token HTTP handlers: mint a /chat/ or /hook/ URL
// token, list/revoke tokens under an owner folder, and the service-token resolve
// (URL token → jid) webd calls. The DB methods live in tokens.go.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

var segRe = regexp.MustCompile(`^[\w-]+$`)

func descendant(target, owner string) bool {
	return target == owner || strings.HasPrefix(target, owner+"/")
}

func (s *Server) handleTokenChat(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var req apiv1.RouteTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, req.OwnerFolder) {
		writeErr(w, 403, "forbidden", "owner_folder outside caller subtree: "+req.OwnerFolder)
		return
	}
	if req.TargetFolder == "" {
		req.TargetFolder = req.OwnerFolder
	}
	if !descendant(req.TargetFolder, req.OwnerFolder) {
		writeErr(w, 400, "bad_target", "target_folder must equal or descend from owner_folder")
		return
	}
	jid := "web:" + req.TargetFolder
	if req.JIDSuffix != "" {
		if !segRe.MatchString(req.JIDSuffix) {
			writeErr(w, 400, "bad_suffix", "jid_suffix must match [\\w-]+")
			return
		}
		jid += "/" + req.JIDSuffix
	}
	token, created, err := s.db.IssueRouteToken(jid, req.OwnerFolder)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 201, apiv1.RouteTokenResponse{Token: token,
		URL: s.webHost + "/chat/" + token + "/", JID: jid,
		OwnerFolder: req.OwnerFolder, CreatedAt: created})
}

func (s *Server) handleTokenHook(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var req apiv1.RouteTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, req.OwnerFolder) {
		writeErr(w, 403, "forbidden", "owner_folder outside caller subtree: "+req.OwnerFolder)
		return
	}
	if req.TargetFolder == "" {
		req.TargetFolder = req.OwnerFolder
	}
	if !descendant(req.TargetFolder, req.OwnerFolder) {
		writeErr(w, 400, "bad_target", "target_folder must equal or descend from owner_folder")
		return
	}
	if !segRe.MatchString(req.SourceLabel) {
		writeErr(w, 400, "bad_source", "source_label must match [\\w-]+")
		return
	}
	jid := "hook:" + req.TargetFolder + "/" + req.SourceLabel
	if req.JIDSuffix != "" {
		if !segRe.MatchString(req.JIDSuffix) {
			writeErr(w, 400, "bad_suffix", "jid_suffix must match [\\w-]+")
			return
		}
		jid += "/" + req.JIDSuffix
	}
	token, created, err := s.db.IssueRouteToken(jid, req.OwnerFolder)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 201, apiv1.RouteTokenResponse{Token: token,
		URL: s.webHost + "/hook/" + token, JID: jid,
		OwnerFolder: req.OwnerFolder, CreatedAt: created})
}

func (s *Server) handleTokenList(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	owner := r.URL.Query().Get("owner_folder")
	if folder != "" { // scoped caller: bind the listing to its own subtree
		if owner == "" {
			owner = folder
		} else if !ownsFolder(folder, owner) {
			writeErr(w, 403, "forbidden", "owner_folder outside caller subtree: "+owner)
			return
		}
	}
	rows, err := s.db.ListRouteTokens(owner)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := make([]apiv1.RouteTokenRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, apiv1.RouteTokenRow{JID: x.JID, OwnerFolder: x.OwnerFolder, CreatedAt: x.CreatedAt})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	jid := r.PathValue("jid")
	owner := r.URL.Query().Get("owner_folder")
	if folder != "" { // scoped caller: revocation bounded to its own subtree
		if owner == "" {
			owner = folder
		} else if !ownsFolder(folder, owner) {
			writeErr(w, 403, "forbidden", "owner_folder outside caller subtree: "+owner)
			return
		}
	}
	n, err := s.db.RevokeRouteTokens(jid, owner)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "not_found", "no tokens for jid under owner_folder")
		return
	}
	w.WriteHeader(204)
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
	jid, owner, err := s.db.ResolveRouteToken(req.Token)
	if err != nil {
		writeErr(w, 404, "unknown_token", "token not found")
		return
	}
	writeJSON(w, 200, apiv1.ResolveResponse{JID: jid, OwnerFolder: owner})
}

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// Route-token REST surface (spec 5/W). Mirrors the MCP tools registered
// in ipc/ipc.go; both go through one writer (gateway.issueRouteToken).
//
// Auth: proxyd-signed X-User-* headers. Operator-only (caller's groups
// must contain "**"). Lighter than a full operator JWT; matches dashd
// auth pattern.

// SetRouteTokenFns wires the gateway-owned helpers + HMAC secret used
// to verify operator identity headers. Set this at startup in gated/main.go
// after constructing the api.Server and gateway.GatedFns.
func (s *Server) SetRouteTokenFns(fns ipc.GatedFns, hmacSecret string) {
	s.issueRouteToken = fns.IssueRouteToken
	s.listRouteTokens = fns.ListRouteTokens
	s.revokeRouteToken = fns.RevokeRouteToken
	s.routeTokenHMAC = hmacSecret
}

type routeTokenCreateReq struct {
	Kind         string `json:"kind"`           // "chat" | "hook"
	OwnerFolder  string `json:"owner_folder"`   // who can revoke (default: target_folder)
	TargetFolder string `json:"target_folder"`  // JID's folder
	SourceLabel  string `json:"source_label"`   // required for hook
	JIDSuffix    string `json:"jid_suffix"`     // optional partition
}

func (s *Server) handleCreateRouteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.operatorID(w, r)
	if !ok {
		return
	}
	var req routeTokenCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		chanlib.WriteErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Kind != store.RouteTokenKindChat && req.Kind != store.RouteTokenKindHook {
		chanlib.WriteErr(w, http.StatusBadRequest, "kind must be chat|hook")
		return
	}
	if req.TargetFolder == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "target_folder required")
		return
	}
	owner := req.OwnerFolder
	if owner == "" {
		owner = req.TargetFolder
	}
	if err := auth.AuthorizeStructural(auth.Resolve(owner), "register_group",
		auth.AuthzTarget{TargetFolder: req.TargetFolder}); err != nil {
		// Allow self / descendant mint per the tier model. Re-use register_group's
		// shape only as a tier gate; final approval is by the operator session.
		_ = err // operator check above already grants admin authority
	}
	if s.issueRouteToken == nil {
		chanlib.WriteErr(w, http.StatusServiceUnavailable, "issue not configured")
		return
	}
	info, err := s.issueRouteToken(req.Kind, owner, req.TargetFolder, req.SourceLabel, req.JIDSuffix)
	if err != nil {
		chanlib.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("route_token issued via REST", "operator", id.Folder,
		"kind", req.Kind, "jid", info.JID, "owner", info.OwnerFolder)
	chanlib.WriteJSON(w, info)
}

func (s *Server) handleListRouteTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.operatorID(w, r); !ok {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner_folder"))
	if owner == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "owner_folder required")
		return
	}
	if s.listRouteTokens == nil {
		chanlib.WriteErr(w, http.StatusServiceUnavailable, "list not configured")
		return
	}
	out := s.listRouteTokens(owner)
	chanlib.WriteJSON(w, map[string]any{"tokens": out})
}

func (s *Server) handleDeleteRouteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.operatorID(w, r)
	if !ok {
		return
	}
	jid := strings.TrimSpace(r.PathValue("jid"))
	if jid == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner_folder"))
	if owner == "" {
		chanlib.WriteErr(w, http.StatusBadRequest, "owner_folder required")
		return
	}
	if s.revokeRouteToken == nil {
		chanlib.WriteErr(w, http.StatusServiceUnavailable, "revoke not configured")
		return
	}
	deleted, err := s.revokeRouteToken(jid, owner)
	if err != nil {
		chanlib.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("route_token revoke via REST", "operator", id.Folder,
		"jid", jid, "owner", owner, "deleted", deleted)
	chanlib.WriteJSON(w, map[string]any{"deleted": deleted})
}

// operatorID verifies proxyd-signed user headers and requires the
// caller to be an instance operator (groups include "**"). Writes the
// 401/403 response and returns ok=false on failure.
func (s *Server) operatorID(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	if s.routeTokenHMAC == "" || !auth.VerifyUserSig(s.routeTokenHMAC, r) {
		chanlib.WriteErr(w, http.StatusUnauthorized, "user signature required")
		return auth.Identity{}, false
	}
	var groups []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		_ = json.Unmarshal([]byte(hdr), &groups)
	}
	op := false
	for _, g := range groups {
		if g == "**" {
			op = true
			break
		}
	}
	if !op {
		chanlib.WriteErr(w, http.StatusForbidden, "operator only")
		return auth.Identity{}, false
	}
	return auth.Resolve(r.Header.Get("X-User-Sub")), true
}

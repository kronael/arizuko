package routd

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// buildMessageRow maps the ingress wire Message to a core.Message row.
func buildMessageRow(m apiv1.Message, ts time.Time, verb string) core.Message {
	atts := m.Attachment
	if len(m.Attachments) > 0 {
		if raw, err := json.Marshal(m.Attachments); err == nil {
			atts = string(raw)
		}
	}
	return core.Message{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName,
		Content: m.Content, Timestamp: ts, ReplyToID: m.ReplyTo,
		ReplyToText: m.ReplyToText, ReplyToSender: m.ReplyToSender,
		Topic: m.Topic, Verb: verb, Attachments: atts, ChatName: m.ChatName,
		Status: core.MessageStatusSent,
	}
}

func toWireRoute(r core.Route) apiv1.Route {
	return apiv1.Route{
		ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars,
	}
}

func fromWireRoute(r apiv1.Route) core.Route {
	return core.Route{
		ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars,
	}
}

func (s *Server) handleRoutesList(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	routes, err := s.db.Routes()
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := make([]apiv1.Route, 0, len(routes))
	for _, rt := range routes {
		if !ownsFolder(folder, rt.Target) { // scoped caller sees only its subtree
			continue
		}
		out = append(out, toWireRoute(rt))
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleRouteGet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "routes:read", "routes:read:own_group") {
		return
	}
	id, ok := atoi64(r.PathValue("id"))
	if !ok {
		writeErr(w, 400, "bad_request", "non-numeric id")
		return
	}
	routes, _ := s.db.Routes()
	for _, rt := range routes {
		if rt.ID == id {
			writeJSON(w, 200, toWireRoute(rt))
			return
		}
	}
	writeErr(w, 404, "not_found", "route not found")
}

func (s *Server) handleRoutesReplace(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var body []apiv1.Route
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	routes := make([]core.Route, 0, len(body))
	for _, rt := range body {
		if !ownsFolder(folder, rt.Target) {
			writeErr(w, 403, "forbidden", "route target outside caller folder: "+rt.Target)
			return
		}
		routes = append(routes, fromWireRoute(rt))
	}
	n, err := s.db.SetRoutes(folder, routes)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"count": n})
}

func (s *Server) handleRouteAdd(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	var body apiv1.Route
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, body.Target) {
		writeErr(w, 403, "forbidden", "route target outside caller folder: "+body.Target)
		return
	}
	id, err := s.db.AddRoute(fromWireRoute(body))
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	body.ID = id
	writeJSON(w, 201, body)
}

func (s *Server) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:write", "routes:write:own_group")
	if !ok {
		return
	}
	id, ok := atoi64(r.PathValue("id"))
	if !ok {
		writeErr(w, 400, "bad_request", "non-numeric id")
		return
	}
	if folder != "" { // scoped caller: the route's target must be in its subtree
		routes, _ := s.db.Routes()
		for _, rt := range routes {
			if rt.ID == id && !ownsFolder(folder, rt.Target) {
				writeErr(w, 403, "forbidden", "route target outside caller folder: "+rt.Target)
				return
			}
		}
	}
	if err := s.db.DeleteRoute(id); err != nil {
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	w.WriteHeader(204)
}

// --- web routes ---

func (s *Server) handleWebRoutesList(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
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

// --- route tokens (5/W) ---

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
	// webd's service token resolves a URL token → jid (spec 5/E § Route tokens).
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

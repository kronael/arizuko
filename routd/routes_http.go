package routd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
		ForwardedFrom: m.ForwardedFrom,
		Topic:         m.Topic, Verb: verb, Attachments: atts, ChatName: m.ChatName,
		// Source = the originating adapter/account; LatestSource(jid) reads it so a
		// reply resolves back to the bot that received it (multi-account routing).
		Source: m.Source,
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
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	id, ok := atoi64(r.PathValue("id"))
	if !ok {
		writeErr(w, 400, "bad_request", "non-numeric id")
		return
	}
	rt, err := s.db.GetRoute(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	if err != nil {
		slog.Error("route lookup failed", "id", id, "err", err)
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !ownsFolder(folder, rt.Target) { // scoped caller can't GET a route outside its subtree
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	writeJSON(w, 200, toWireRoute(rt))
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
		routes, err := s.db.Routes()
		if err != nil {
			writeErr(w, 500, "store_error", err.Error())
			return
		}
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

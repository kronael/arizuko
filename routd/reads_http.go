package routd

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// reads_http.go serves the agent's read/manage surface that runed federates
// the StoreFns to (spec P-runed § federation: message reads + routing →
// routd). These are NOT turn-scoped: the agent calls them with its brokered
// capability token directly; authz bounds reads to the token's folder claim.

// scopeRoutesRead/Write match the route-CRUD scopes; engagement is a
// routing-state write owned by the caller's folder.
var (
	scopeRoutesRead  = []string{"routes:read", "routes:read:own_group"}
	scopeRoutesWrite = []string{"routes:write", "routes:write:own_group"}
)

func toMessageRow(m core.Message) apiv1.MessageRow {
	return apiv1.MessageRow{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, SenderName: m.Name,
		Content: m.Content, Timestamp: m.Timestamp.UTC().Format(time.RFC3339Nano),
		IsFromMe: m.FromMe, IsBotMsg: m.BotMsg, ReplyToID: m.ReplyToID,
		Topic: m.Topic, RoutedTo: m.RoutedTo, Verb: m.Verb, Source: m.Source,
		Status: m.Status, PlatformID: m.PlatformID, ChatName: m.ChatName,
		ForwardedFrom: m.ForwardedFrom,
	}
}

func messageRows(msgs []core.Message) []apiv1.MessageRow {
	out := make([]apiv1.MessageRow, len(msgs))
	for i, m := range msgs {
		out[i] = toMessageRow(m)
	}
	return out
}

func (s *Server) handleInspectMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRead...) {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.db.MessagesBefore(jid, r.URL.Query().Get("before"), limit)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.MessagesResponse{Messages: messageRows(msgs), Count: len(msgs)})
}

func (s *Server) handleThreadMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRead...) {
		return
	}
	jid := r.URL.Query().Get("jid")
	topic := r.URL.Query().Get("topic")
	if jid == "" || topic == "" {
		writeErr(w, 400, "bad_request", "jid and topic required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.db.MessagesByThread(jid, topic, r.URL.Query().Get("before"), limit)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.MessagesResponse{Messages: messageRows(msgs), Count: len(msgs)})
}

func (s *Server) handleFindMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRead...) {
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		writeErr(w, 400, "bad_request", "query required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.db.FindMessages(q, r.URL.Query().Get("scope"),
		r.URL.Query().Get("sender"), r.URL.Query().Get("since"), limit)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.FindResponse{Messages: make([]apiv1.FoundMessage, len(hits)), Count: len(hits)}
	for i, h := range hits {
		out.Messages[i] = apiv1.FoundMessage{
			ChatJID: h.ChatJID, Sender: h.Sender, Content: h.Content, Rank: h.Rank,
			IsFromMe: h.IsFromMe, IsBotMessage: h.IsBotMessage,
			Timestamp: h.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	}
	writeJSON(w, 200, out)
}

// --- routing resolution ---

func (s *Server) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRoutesRead...) {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	if folder := r.URL.Query().Get("folder"); folder != "" {
		writeJSON(w, 200, apiv1.RoutingResolveResponse{Routed: s.db.JIDRoutedToFolder(jid, folder)})
		return
	}
	writeJSON(w, 200, apiv1.RoutingResolveResponse{Folder: s.db.DefaultFolderForJID(jid)})
}

func (s *Server) handleErroredChats(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, scopeRoutesRead...)
	if !ok {
		return
	}
	q := r.URL.Query().Get("folder")
	if q == "" {
		q = folder
	}
	chats, err := s.db.ErroredChats(q, q == "")
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.ErroredChatsResponse{Chats: make([]apiv1.ErroredChat, len(chats))}
	for i, c := range chats {
		out.Chats[i] = apiv1.ErroredChat{ChatJID: c.ChatJID, Count: c.Count,
			RoutedTo: c.RoutedTo, LastAt: c.LastAt.UTC().Format(time.RFC3339Nano)}
	}
	writeJSON(w, 200, out)
}

// --- engagement ---

func (s *Server) handleEngagementGet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRoutesRead...) {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	topic := r.URL.Query().Get("topic")
	folder, _ := s.db.Engaged(jid, topic)
	writeJSON(w, 200, apiv1.EngagementResponse{Folder: folder, LastReplyID: s.db.LastReplyID(jid, topic)})
}

func (s *Server) handleEngagementSet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRoutesWrite...) {
		return
	}
	var req apiv1.EngagementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if req.JID == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	// TTLSeconds<=0 is the disengage path: SetEngagement with a zero/past
	// deadline clears the live window (Engaged checks until > now).
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = -time.Second
	}
	if err := s.db.SetEngagement(req.JID, req.Topic, req.Folder, ttl); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// --- sessions (resume id) ---

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeRead...) {
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		writeErr(w, 400, "bad_request", "folder required")
		return
	}
	writeJSON(w, 200, apiv1.SessionResponse{SessionID: s.db.SessionID(folder, r.URL.Query().Get("topic"))})
}

// --- external cost ---

func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, scopeSend...) {
		return
	}
	var req apiv1.CostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if err := s.db.LogExternalCost(req.Folder, req.Provider, req.Model,
		req.InputTokens, req.OutputTokens, req.CostCents); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

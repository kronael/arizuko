package routd

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// reads_http.go is routd's REST read/manage surface (message reads + routing +
// cost; engagement has its own file, engagement_http.go) — the REST twin of the agent's in-process MCP StoreFns
// (routd/mcp.go), for humans / external tools. NOT turn-scoped; authz bounds
// reads to the bearer token's folder claim.

// scopeRoutesRead/Write match the route-CRUD scopes; engagement is a
// routing-state write owned by the caller's folder.
var (
	scopeRoutesRead  = []string{"routes:read", "routes:read:own_group"}
	scopeRoutesWrite = []string{"routes:write", "routes:write:own_group"}
	// External-cost accounting is its own capability — NOT messages:send (codex
	// review: don't couple cost-write to outbound conversation). Agents log cost
	// via the in-process MCP log_external_cost tool; this REST path is for service
	// callers that hold cost:write.
	scopeCost = []string{"cost:write", "cost:write:own_group"}
	// Reading a turn's spend is the read half of the same capability (anteval's
	// token budgets, operator cost tooling) — cost:read, not messages:read.
	scopeCostRead = []string{"cost:read", "cost:read:own_group"}
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
	_, folder, ok := s.authz(w, r, scopeRead...)
	if !ok {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	if !s.ownsJID(folder, jid) {
		denyCrossFolder(w, jid)
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
	_, folder, ok := s.authz(w, r, scopeRead...)
	if !ok {
		return
	}
	jid := r.URL.Query().Get("jid")
	topic := r.URL.Query().Get("topic")
	if jid == "" || topic == "" {
		writeErr(w, 400, "bad_request", "jid and topic required")
		return
	}
	if !s.ownsJID(folder, jid) {
		denyCrossFolder(w, jid)
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
	_, folder, ok := s.authz(w, r, scopeRead...)
	if !ok {
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		writeErr(w, 400, "bad_request", "query required")
		return
	}
	// Confine a folder-scoped caller to its subtree: empty scope defaults to the
	// token folder; an explicit scope (chat jid or folder subtree) must be owned.
	scope := r.URL.Query().Get("scope")
	if folder != "" {
		switch {
		case scope == "":
			scope = folder
		case strings.Contains(scope, ":"):
			if !s.ownsJID(folder, scope) {
				denyCrossFolder(w, scope)
				return
			}
		default:
			if !ownsFolder(folder, scope) {
				denyCrossFolder(w, scope)
				return
			}
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.db.FindMessages(q, scope,
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

func (s *Server) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
	_, tokenFolder, ok := s.authz(w, r, scopeRoutesRead...)
	if !ok {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	if !s.ownsJID(tokenFolder, jid) {
		denyCrossFolder(w, jid)
		return
	}
	if folder := r.URL.Query().Get("folder"); folder != "" {
		if !ownsFolder(tokenFolder, folder) {
			denyCrossFolder(w, folder)
			return
		}
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
	if !ownsFolder(folder, q) {
		denyCrossFolder(w, q)
		return
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

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	_, tokenFolder, ok := s.authz(w, r, scopeRead...)
	if !ok {
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		writeErr(w, 400, "bad_request", "folder required")
		return
	}
	if !ownsFolder(tokenFolder, folder) {
		denyCrossFolder(w, folder)
		return
	}
	writeJSON(w, 200, apiv1.SessionResponse{SessionID: s.db.SessionID(folder, r.URL.Query().Get("topic"))})
}

func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, scopeCost...)
	if !ok {
		return
	}
	var req apiv1.CostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if !ownsFolder(folder, req.Folder) {
		denyCrossFolder(w, req.Folder)
		return
	}
	if err := s.db.LogExternalCost(req.Folder, req.Provider, req.Model,
		req.InputTokens, req.OutputTokens, req.CostCents); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// handleCostGet is the read twin of the cost surface: one turn's spend summed
// across models. Closes spec 5/9's honest gap (a) — before this, cost was
// write-only over REST, so external budget checks (anteval max_tokens) read 0.
func (s *Server) handleCostGet(w http.ResponseWriter, r *http.Request) {
	_, tokenFolder, ok := s.authz(w, r, scopeCostRead...)
	if !ok {
		return
	}
	turnID := r.URL.Query().Get("turn_id")
	if turnID == "" {
		writeErr(w, 400, "bad_request", "turn_id required")
		return
	}
	folder, in, out, cents, found, err := s.db.CostByTurn(turnID)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !found {
		writeErr(w, 404, "not_found", "no cost recorded for turn "+turnID)
		return
	}
	if !ownsFolder(tokenFolder, folder) {
		denyCrossFolder(w, folder)
		return
	}
	writeJSON(w, 200, apiv1.CostResponse{
		TurnID: turnID, Folder: folder,
		InputTokens: in, OutputTokens: out, CostCents: cents,
	})
}

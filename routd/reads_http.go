package routd

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/routd/msgsearch"
)

// reads_http.go is routd's REST read/manage surface — the REST twin of the
// agent's in-process MCP StoreFns (routd/mcp.go), for humans / external tools.
// NOT turn-scoped; authz bounds reads to the bearer token's folder claim.

var (
	scopeRead        = []string{"chats:read:own_group", "chats:read", "messages:read"}
	scopeRoutesRead  = []string{"routes:read", "routes:read:own_group"}
	scopeRoutesWrite = []string{"routes:write", "routes:write:own_group"}
	// Cost accounting is its own capability, NOT messages:send: cost-write is
	// never coupled to outbound conversation.
	scopeCost     = []string{"cost:write", "cost:write:own_group"}
	scopeCostRead = []string{"cost:read", "cost:read:own_group"}
)

func toMessageRow(m core.Message) apiv1.MessageRow {
	return apiv1.MessageRow{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, SenderName: m.Name,
		Content: m.Content, Timestamp: m.Timestamp.UTC().Format(time.RFC3339Nano),
		IsFromMe: m.FromMe, IsBotMsg: m.BotMsg, ReplyToID: m.ReplyToID,
		Topic: m.Topic, RoutedTo: m.RoutedTo, Verb: m.Verb, Source: m.Source,
		PlatformID: m.PlatformID, ChatName: m.ChatName,
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
	// A folder-scoped caller is confined to its subtree: an empty scope
	// defaults to the token folder; an explicit one must be owned.
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
		// A warming index is a wait, not a fault: unavailable, not broken.
		if errors.Is(err, msgsearch.ErrWarming) {
			writeErr(w, 503, "index_warming", err.Error())
			return
		}
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

// handleCostSummary answers GET /v1/cost/summary?folder=&since= — what the
// gateway metered per folder over a window (spec 6/39), the one place every
// operator surface reaches the ledger. An instance with no gateway says so
// rather than reporting a zero that reads like "nothing was spent". The folder
// claim narrows the result as every other read here does.
func (s *Server) handleCostSummary(w http.ResponseWriter, r *http.Request) {
	_, tokenFolder, ok := s.authz(w, r, scopeCostRead...)
	if !ok {
		return
	}
	folder := r.URL.Query().Get("folder")
	if !ownsFolder(tokenFolder, folder) {
		writeErr(w, 403, "forbidden", "folder outside token scope")
		return
	}
	if folder == "" {
		folder = tokenFolder
	}
	since := r.URL.Query().Get("since")
	if since == "" {
		// A summary with no window would report a lifetime total as current.
		writeErr(w, 400, "bad_request", "since required (RFC3339)")
		return
	}
	gw := s.gateway()
	if gw == nil {
		writeJSON(w, 200, apiv1.CostSummaryResponse{Metered: false, Folders: []apiv1.FolderSpend{}})
		return
	}
	rows, err := gw.spendByFolder(r.Context(), gatewayDay(since), gatewayDay(core.TS(time.Now())))
	if err != nil {
		writeErr(w, 502, "gateway_error", err.Error())
		return
	}
	out := make([]apiv1.FolderSpend, 0, len(rows))
	for _, row := range rows {
		if !ownsFolder(folder, row.Folder) {
			continue
		}
		out = append(out, apiv1.FolderSpend{
			Folder: row.Folder, Tokens: row.Tokens, CostCents: row.CostCents,
		})
	}
	writeJSON(w, 200, apiv1.CostSummaryResponse{Metered: true, Folders: out})
}

// gatewayDay reduces an RFC3339 instant to the YYYY-MM-DD the gateway's daily
// activity read is bounded by.
func gatewayDay(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// handleCostGet answers GET /v1/cost?turn_id= — what the gateway charged the
// key that ran one turn; arizuko stores the pointer, never the number (spec
// 6/39). 404: the turn ran with no gateway, or the gateway has not flushed its
// usage rows yet.
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
	tc, found := s.db.GetTurnContext(turnID)
	if !found {
		writeErr(w, 404, "not_found", "no such turn "+turnID)
		return
	}
	if !ownsFolder(tokenFolder, tc.Folder) {
		denyCrossFolder(w, tc.Folder)
		return
	}
	hash, err := s.db.TurnGatewayKey(turnID)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	gw := s.gateway()
	if hash == "" || gw == nil {
		writeErr(w, 404, "not_found", "no meter recorded this turn "+turnID)
		return
	}
	spend, err := gw.keySpend(r.Context(), hash)
	if err != nil {
		writeErr(w, 502, "gateway_error", err.Error())
		return
	}
	if spend.Requests == 0 {
		writeErr(w, 404, "not_found", "no cost recorded for turn "+turnID)
		return
	}
	writeJSON(w, 200, apiv1.CostResponse{
		TurnID: turnID, Folder: tc.Folder,
		InputTokens: spend.InputTokens, OutputTokens: spend.OutputTokens,
		CostCents: spend.CostCents,
	})
}

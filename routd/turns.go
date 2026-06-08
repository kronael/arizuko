package routd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// The /v1/turns/{turn_id}/* callback surface — the HTTP twin of the agent's
// in-process MCP write tools (the agent uses the socket; this is the REST face +
// test surface). reply/send/document append a messages row then deliver;
// like/edit/delete/pin/unpin mutate an existing platform message without
// appending. Every call is idempotent on X-Idempotency-Key, serialized per
// turn_id.

// Scope sets for the turn-callback surface. reply/send/document/like/edit/delete/
// pin/unpin are message writes; history is a read. Each set lists the agent
// (own_group) scope plus the broader operator/service scopes that also grant it
// (any-of match).
var (
	scopeSend = []string{"messages:send:own_group", "messages:send", "messages:write"}
	scopeRead = []string{"chats:read:own_group", "chats:read", "messages:read"}
)

// turnLock serializes append-and-deliver per turn_id so out-of-order arrivals
// append in receive order.
var turnLocks sync.Map // turn_id -> *sync.Mutex

func lockTurn(turnID string) func() {
	m, _ := turnLocks.LoadOrStore(turnID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// idem wraps a turn-command handler in the idempotency ledger. endpoint is the
// path TEMPLATE with vars collapsed (e.g. "POST /v1/turns/reply"), NOT the filled
// path — the per-turn id would partition the ledger. exec returns the HTTP
// (status, resp) and, when the call appends a bot row, that row — idem persists
// the row AND finishes the ledger in ONE tx so a crash between them can't leave a
// permanent in_flight.
func (s *Server) idem(w http.ResponseWriter, r *http.Request, endpoint string, required bool, exec func(body []byte) (int, any, *core.Message)) {
	body, _ := io.ReadAll(r.Body)
	key := r.Header.Get("X-Idempotency-Key")
	if key == "" {
		if required {
			writeErr(w, 400, "missing_idempotency_key", "X-Idempotency-Key required")
			return
		}
		// at-least-once: execute without ledger.
		status, resp, row := exec(body)
		if row != nil {
			_ = s.db.PutMessage(*row)
		}
		writeJSON(w, status, resp)
		return
	}
	reqHash := canonicalHash(body)
	claimed, prior, err := s.db.IdemClaim(endpoint, key, reqHash)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !claimed {
		if prior.RequestHash != reqHash {
			writeErr(w, 409, "idempotency_key_reuse", "key reused with a different body")
			return
		}
		if prior.Status == 0 {
			// in-flight first writer; treat as conflict-retry
			writeErr(w, 409, "in_flight", "request in flight")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(prior.Status)
		_, _ = w.Write([]byte(prior.Response))
		return
	}
	status, resp, row := exec(body)
	raw, _ := json.Marshal(resp)
	// Persist the row (if any) AND finish the ledger atomically — a crash
	// between can't leave a permanent in_flight. A commit failure means the
	// bot row + ledger are NOT durable: report store_error, not the success
	// resp, so the caller (runed) retries on the same key (still in_flight).
	if err := s.db.AppendAndFinish(row, endpoint, key, status, string(raw)); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, status, resp)
}

// canonicalHash re-marshals body with sorted keys so encoder differences don't
// produce false 409s.
func canonicalHash(body []byte) string {
	var v any
	if json.Unmarshal(body, &v) == nil {
		if c, err := json.Marshal(v); err == nil {
			h := sha256.Sum256(c)
			return hex.EncodeToString(h[:])
		}
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// callbackClosed reports whether a turn's callback surface is closed: the
// done-guard fires only AFTER POST /v1/runs returns (run_returned), so a
// still-live run's trailing frames stay valid even past an early submit_turn
// that flipped state→done.
func (s *Server) callbackClosed(tc TurnContext) bool { return tc.RunReturned }

// returnTarget redirects a turn's outbound delivery to the delegation
// return-address (the trigger batch's forwarded_from) when set, so a delegated
// reply returns to the ORIGIN chat instead of the child folder JID the run
// addresses.
func returnTarget(tc TurnContext, jid string) string {
	if tc.ReturnTo != "" {
		return tc.ReturnTo
	}
	return jid
}

// authzTurn gates a turn-callback handler: the bearer token must carry one of
// anyScope AND its arz/folder claim must own the turn's folder. The brokered
// agent token authd mints for a run is bound to that run's group folder, so a
// token for turn A cannot drive turn B in another folder. Fails CLOSED: an
// unknown turn under a scoped token is 403 (a valid token must not probe turn
// existence outside its subtree). verify==nil (local-dev) is open.
func (s *Server) authzTurn(w http.ResponseWriter, r *http.Request, turnID string, anyScope ...string) bool {
	_, folder, ok := s.authz(w, r, anyScope...)
	if !ok {
		return false
	}
	if folder == "" {
		return true // open mode / unscoped service token
	}
	tc, found := s.db.GetTurnContext(turnID)
	if !found || !ownsFolder(folder, tc.Folder) {
		writeErr(w, 403, "forbidden", "turn not owned by caller folder")
		return false
	}
	return true
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/reply", true, func(body []byte) (int, any, *core.Message) {
		var req apiv1.ReplyRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid required"}, nil
		}
		return s.appendAndDeliver(turnID, req.JID, req.Text, req.ReplyToID, true)
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/send", true, func(body []byte) (int, any, *core.Message) {
		var req apiv1.ReplyRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid required"}, nil
		}
		return s.appendAndDeliver(turnID, req.JID, req.Text, "", false)
	})
}

// appendAndDeliver renders the agent's raw output then appends + delivers it:
// strip <think>, surface each <status> as a separate "⏳ ..." progress message,
// then FormatOutbound the remainder as the threaded reply. A pure-think / silent
// output produces no reply row (200, nil). Status rows are persisted inline
// (auxiliary progress); the reply row is returned to idem for the atomic
// append+ledger-finish. threaded replies thread to the active topic's
// last_reply_id when reply_to_id is empty. SEND_DISABLED_GROUPS folders persist
// the row status=sent but skip the platform send.
func (s *Server) appendAndDeliver(turnID, jid, text, replyToID string, threaded bool) (int, any, *core.Message) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context for turn_id"}, nil
	}
	if s.callbackClosed(tc) {
		return 409, apiv1.Err{Error: "turn_done", Message: "turn already terminal"}, nil
	}
	jid = returnTarget(tc, jid)

	stripped, statuses := router.ExtractStatusBlocks(router.StripThinkBlocks(text))
	// Interim progress notices: delivered + persisted out-of-band, not threaded
	// and not the returned reply row.
	for _, st := range statuses {
		row := s.outboundRow(tc, jid, "⏳ "+st, "")
		s.deliverRow(tc, jid, &row)
		_ = s.db.PutMessage(row)
	}

	clean := router.FormatOutbound(stripped)
	if clean == "" {
		// Pure <think> / silent refusal: nothing user-visible to send.
		return 200, apiv1.SendResult{Status: core.MessageStatusSent}, nil
	}

	if threaded && replyToID == "" {
		replyToID = s.db.LastReplyID(jid, tc.Topic)
	}
	row := s.outboundRow(tc, jid, clean, replyToID)
	_ = s.db.SetLastReply(jid, tc.Topic, row.ID, tc.Folder)
	if !strings.HasPrefix(tc.Trigger, "timed-") {
		// Engagement is claimed on the DISPATCH chat (tc.ChatJID), not the
		// delegation-substituted delivery target — a delegated reply must engage
		// the chat that triggered the turn, not the origin JID the reply returns to.
		_ = s.db.SetEngagement(tc.ChatJID, tc.Topic, tc.Folder, s.engagementT)
	}
	s.deliverRow(tc, jid, &row)
	return 200, apiv1.SendResult{MessageID: row.ID, PlatformID: row.PlatformID, Status: row.Status}, &row
}

// outboundRow builds a fresh pending bot row for the turn's folder/topic.
func (s *Server) outboundRow(tc TurnContext, jid, content, replyToID string) core.Message {
	return core.Message{
		ID: "out-" + randHex(8), ChatJID: jid, Sender: tc.Folder, Content: content,
		Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true,
		ReplyToID: replyToID, Topic: tc.Topic, RoutedTo: tc.Folder,
		TurnID: tc.TurnID, Status: core.MessageStatusPending,
	}
}

// deliverRow attempts the platform send and stamps platform_id+sent on success.
// A SEND_DISABLED_GROUPS folder skips the send entirely but still lands the row
// status=sent so the poll loop never retries it.
func (s *Server) deliverRow(tc TurnContext, jid string, row *core.Message) {
	if s.mutedGroup(tc.Folder) {
		row.Status = core.MessageStatusSent
		return
	}
	if s.deliver == nil {
		return
	}
	if pid, err := s.deliver.Send(jid, row.Content, row.ReplyToID, tc.Topic, row.ID); err == nil {
		row.PlatformID = pid
		row.Status = core.MessageStatusSent
	}
}

func (s *Server) handleDocument(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/document", true, func(body []byte) (int, any, *core.Message) {
		var req apiv1.DocumentRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" || req.Path == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid and path required"}, nil
		}
		unlock := lockTurn(turnID)
		defer unlock()
		tc, ok := s.db.GetTurnContext(turnID)
		if !ok {
			return 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context"}, nil
		}
		if s.callbackClosed(tc) {
			return 409, apiv1.Err{Error: "turn_done", Message: "turn already terminal"}, nil
		}
		jid := returnTarget(tc, req.JID)
		msgID := "out-" + randHex(8)
		row := core.Message{ID: msgID, ChatJID: jid, Sender: tc.Folder,
			Content: req.Caption, Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true,
			Topic: tc.Topic, RoutedTo: tc.Folder, TurnID: turnID, Status: core.MessageStatusPending}
		if s.deliver != nil {
			if pid, err := s.deliver.Document(jid, req.Path, req.Name, req.Caption, req.ReplyToID, msgID); err == nil {
				row.PlatformID = pid
				row.Status = core.MessageStatusSent
			}
		}
		return 200, apiv1.SendResult{MessageID: msgID, PlatformID: row.PlatformID, Status: row.Status}, &row
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeRead...) {
		return
	}
	jid := r.URL.Query().Get("jid")
	before := r.URL.Query().Get("before")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.db.History(jid, before, limit)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.HistoryResponse{Source: "cache", Cap: limit}
	for _, m := range msgs {
		out.Messages = append(out.Messages, apiv1.HistoryMessage{
			ID: m.ID, Sender: m.Sender, Content: m.Content,
			Timestamp: m.Timestamp.UTC().Format(time.RFC3339Nano),
			ReplyToID: m.ReplyToID, IsFromMe: m.FromMe, IsBotMsg: m.BotMsg, PlatformID: m.PlatformID,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleLike(w http.ResponseWriter, r *http.Request) {
	s.mutate(w, r, "POST /v1/turns/like", func(req apiv1.ReactionRequest) error {
		if s.deliver == nil {
			return nil
		}
		return s.deliver.React(req.JID, req.PlatformID, req.Reaction)
	})
}

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/edit", false, func(body []byte) (int, any, *core.Message) {
		if status, errResp := s.guardOpen(turnID); errResp != nil {
			return status, errResp, nil
		}
		var req apiv1.EditRequest
		json.Unmarshal(body, &req)
		if s.deliver != nil {
			if err := s.deliver.Edit(req.JID, req.PlatformID, req.Content); err != nil {
				return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}, nil
			}
		}
		return 200, apiv1.OK{OK: true}, nil
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	s.target(w, r, "POST /v1/turns/delete", func(req apiv1.TargetRequest) error {
		if s.deliver == nil {
			return nil
		}
		return s.deliver.Delete(req.JID, req.PlatformID)
	})
}

func (s *Server) handlePin(w http.ResponseWriter, r *http.Request) {
	s.target(w, r, "POST /v1/turns/pin", func(req apiv1.TargetRequest) error {
		if s.deliver == nil {
			return nil
		}
		return s.deliver.Pin(req.JID, req.PlatformID)
	})
}

func (s *Server) handleUnpin(w http.ResponseWriter, r *http.Request) {
	s.target(w, r, "POST /v1/turns/unpin", func(req apiv1.TargetRequest) error {
		if s.deliver == nil {
			return nil
		}
		return s.deliver.Unpin(req.JID, req.PlatformID, req.All)
	})
}

// guardOpen returns a 409 turn_done error for a closed turn (run-response
// returned), else (0, nil). The mutation tools (like/edit/delete/pin/unpin) run
// it so a late frame doesn't mutate a platform message after the run is no longer
// live.
func (s *Server) guardOpen(turnID string) (int, any) {
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context for turn_id"}
	}
	if s.callbackClosed(tc) {
		return 409, apiv1.Err{Error: "turn_done", Message: "turn already terminal"}
	}
	return 0, nil
}

func (s *Server) mutate(w http.ResponseWriter, r *http.Request, endpoint string, fn func(apiv1.ReactionRequest) error) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, endpoint, false, func(body []byte) (int, any, *core.Message) {
		if status, errResp := s.guardOpen(turnID); errResp != nil {
			return status, errResp, nil
		}
		var req apiv1.ReactionRequest
		json.Unmarshal(body, &req)
		if err := fn(req); err != nil {
			return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}, nil
		}
		return 200, apiv1.OK{OK: true}, nil
	})
}

func (s *Server) target(w http.ResponseWriter, r *http.Request, endpoint string, fn func(apiv1.TargetRequest) error) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, endpoint, false, func(body []byte) (int, any, *core.Message) {
		if status, errResp := s.guardOpen(turnID); errResp != nil {
			return status, errResp, nil
		}
		var req apiv1.TargetRequest
		json.Unmarshal(body, &req)
		if err := fn(req); err != nil {
			return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}, nil
		}
		return 200, apiv1.OK{OK: true}, nil
	})
}

// handleResult is submit_turn's REST twin. Records the outcome idempotently into
// turn_results, persists session_id + cost on the FIRST record, flips
// turn_context to done. It does NOT set run_returned: the run may still emit
// trailing frames until POST /v1/runs returns, and those callbacks stay valid.
func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	var req apiv1.TurnResult
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if _, ok := s.db.GetTurnContext(turnID); !ok {
		writeErr(w, 409, "unknown_turn", "no turn context for turn_id")
		return
	}
	first, err := s.recordTurnResult(turnID, req)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.TurnResultAck{Recorded: first})
}

// recordTurnResult is the shared turn-completion writer behind both the REST
// /result twin and the in-process submit_turn MCP method. It records the
// outcome idempotently, and on the FIRST record persists session_id + per-model
// cost, flips the turn to done, and publishes round_done. Returns whether this
// was the first record. The turn context must already exist (the callers check).
func (s *Server) recordTurnResult(turnID string, req apiv1.TurnResult) (bool, error) {
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return false, fmt.Errorf("no turn context for turn_id")
	}
	first, err := s.db.RecordTurnResult(tc.Folder, turnID, req.SessionID, req.Status)
	if err != nil {
		return false, err
	}
	if first {
		// Deliver the agent's prose result as the reply when it called no
		// reply/send tool this turn. The agent's contract (ant/CLAUDE.md: "text
		// outside <think> is delivered") relies on submit_turn's result reaching
		// the user; the split had dropped it, so a normal Q&A turn produced output
		// that never left routd (krons no-reply outage 2026-06-08). Same
		// strip-think/format/deliver path as an explicit reply; skipped when the
		// agent already delivered a row (no double-send), and BEFORE marking the
		// turn done so the callback isn't yet closed.
		if req.Result != "" && !s.db.TurnHasBotReply(turnID) {
			if _, _, row := s.appendAndDeliver(turnID, tc.ChatJID, req.Result, "", true); row != nil {
				_ = s.db.PutMessage(*row)
			}
		}
		if req.SessionID != "" {
			_ = s.db.PutSession(tc.Folder, tc.Topic, req.SessionID)
		}
		userSub := callerSubOfMsg(tc.Trigger)
		for model, c := range req.Models {
			_ = s.db.PutCost(tc.Folder, turnID, userSub, model, c.Input, c.Output, c.CostCents)
		}
		_ = s.db.SetTurnState(turnID, "done")
		if s.loop != nil {
			s.loop.publishRoundDone(strings.TrimPrefix(tc.ChatJID, "web:"), turnID)
		}
	}
	return first, nil
}

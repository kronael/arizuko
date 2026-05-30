package routd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// The /v1/turns/{turn_id}/* callback surface — the sole-appender entry
// point the agent's tools (federated by runed) call back into. reply/send/
// document append a messages row then deliver; like/edit/delete/pin/unpin
// mutate an existing platform message without appending. Every call is
// idempotent on X-Idempotency-Key, serialized per turn_id.

// turnLock serializes append-and-deliver per turn_id so out-of-order
// arrivals append in receive order (spec § Per-turn callback
// serialization).
var turnLocks sync.Map // turn_id -> *sync.Mutex

func lockTurn(turnID string) func() {
	m, _ := turnLocks.LoadOrStore(turnID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// idem wraps a turn-command handler in the idempotency ledger. endpoint is
// the path TEMPLATE with vars collapsed (e.g. "POST /v1/turns/reply"), NOT
// the filled path — the per-turn id would partition the ledger. Returns
// (handled=true) when the call was a replay (response already written).
func (s *Server) idem(w http.ResponseWriter, r *http.Request, endpoint string, required bool, exec func(body []byte) (int, any)) {
	body, _ := io.ReadAll(r.Body)
	key := r.Header.Get("X-Idempotency-Key")
	if key == "" {
		if required {
			writeErr(w, 400, "missing_idempotency_key", "X-Idempotency-Key required")
			return
		}
		// at-least-once: execute without ledger
		status, resp := exec(body)
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
	status, resp := exec(body)
	raw, _ := json.Marshal(resp)
	_ = s.db.IdemFinish(endpoint, key, status, string(raw))
	writeJSON(w, status, resp)
}

// canonicalHash re-marshals body with sorted keys so encoder differences
// don't produce false 409s (spec § Idempotency canonical body).
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

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	turnID := r.PathValue("turn_id")
	s.idem(w, r, "POST /v1/turns/reply", true, func(body []byte) (int, any) {
		var req apiv1.ReplyRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid required"}
		}
		return s.appendAndDeliver(turnID, req.JID, req.Text, req.ReplyToID, true)
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	turnID := r.PathValue("turn_id")
	s.idem(w, r, "POST /v1/turns/send", true, func(body []byte) (int, any) {
		var req apiv1.ReplyRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid required"}
		}
		return s.appendAndDeliver(turnID, req.JID, req.Text, "", false)
	})
}

// appendAndDeliver writes the bot row status=pending, attempts delivery,
// and on success marks platform_id+sent. threaded replies thread to the
// active topic's last_reply_id when reply_to_id is empty. Writes
// SetLastReply (always) + BumpEngagement (unless timed-* trigger).
func (s *Server) appendAndDeliver(turnID, jid, text, replyToID string, threaded bool) (int, any) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context for turn_id"}
	}
	if tc.State == "done" {
		return 409, apiv1.Err{Error: "turn_done", Message: "turn already terminal"}
	}
	if threaded && replyToID == "" {
		replyToID = s.db.LastReplyID(jid, tc.Topic)
	}
	msgID := "out-" + randHex(8)
	row := core.Message{
		ID: msgID, ChatJID: jid, Sender: tc.Folder, Content: text,
		Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true,
		ReplyToID: replyToID, Topic: tc.Topic, RoutedTo: tc.Folder,
		TurnID: turnID, Status: core.MessageStatusPending,
	}
	if err := s.db.PutMessage(row); err != nil {
		return 500, apiv1.Err{Error: "store_error", Message: err.Error()}
	}
	_ = s.db.SetLastReply(jid, tc.Topic, msgID, tc.Folder)
	if !strings.HasPrefix(tc.Trigger, "timed-") {
		_ = s.db.SetEngagement(jid, tc.Topic, tc.Folder, s.engagementT)
	}
	status := core.MessageStatusPending
	platformID := ""
	if s.deliver != nil {
		pid, err := s.deliver.Send(jid, text, replyToID, tc.Topic, msgID)
		if err == nil {
			platformID = pid
			status = core.MessageStatusSent
			_ = s.db.MarkBotPlatformID(msgID, pid)
		}
	}
	return 200, apiv1.SendResult{MessageID: msgID, PlatformID: platformID, Status: status}
}

func (s *Server) handleDocument(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	turnID := r.PathValue("turn_id")
	s.idem(w, r, "POST /v1/turns/document", true, func(body []byte) (int, any) {
		var req apiv1.DocumentRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" || req.Path == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid and path required"}
		}
		unlock := lockTurn(turnID)
		defer unlock()
		tc, ok := s.db.GetTurnContext(turnID)
		if !ok {
			return 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context"}
		}
		msgID := "out-" + randHex(8)
		row := core.Message{ID: msgID, ChatJID: req.JID, Sender: tc.Folder,
			Content: req.Caption, Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true,
			Topic: tc.Topic, RoutedTo: tc.Folder, TurnID: turnID, Status: core.MessageStatusPending}
		if err := s.db.PutMessage(row); err != nil {
			return 500, apiv1.Err{Error: "store_error", Message: err.Error()}
		}
		status := core.MessageStatusPending
		if s.deliver != nil {
			if _, err := s.deliver.Document(req.JID, req.Path, req.Name, req.Caption, req.ReplyToID, msgID); err == nil {
				status = core.MessageStatusSent
				_ = s.db.MarkStatus(msgID, status)
			}
		}
		return 200, apiv1.SendResult{MessageID: msgID, Status: status}
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
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
	if !s.authed(w, r) {
		return
	}
	s.idem(w, r, "POST /v1/turns/edit", false, func(body []byte) (int, any) {
		var req apiv1.EditRequest
		json.Unmarshal(body, &req)
		if s.deliver != nil {
			if err := s.deliver.Edit(req.JID, req.PlatformID, req.Content); err != nil {
				return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}
			}
		}
		return 200, apiv1.OK{OK: true}
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

func (s *Server) mutate(w http.ResponseWriter, r *http.Request, endpoint string, fn func(apiv1.ReactionRequest) error) {
	if !s.authed(w, r) {
		return
	}
	s.idem(w, r, endpoint, false, func(body []byte) (int, any) {
		var req apiv1.ReactionRequest
		json.Unmarshal(body, &req)
		if err := fn(req); err != nil {
			return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}
		}
		return 200, apiv1.OK{OK: true}
	})
}

func (s *Server) target(w http.ResponseWriter, r *http.Request, endpoint string, fn func(apiv1.TargetRequest) error) {
	if !s.authed(w, r) {
		return
	}
	s.idem(w, r, endpoint, false, func(body []byte) (int, any) {
		var req apiv1.TargetRequest
		json.Unmarshal(body, &req)
		if err := fn(req); err != nil {
			return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}
		}
		return 200, apiv1.OK{OK: true}
	})
}

// handleResult is submit_turn's REST twin. Records the outcome
// idempotently into turn_results, persists session_id + cost on the FIRST
// record, flips turn_context to done.
func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	turnID := r.PathValue("turn_id")
	var req apiv1.TurnResult
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		writeErr(w, 409, "unknown_turn", "no turn context for turn_id")
		return
	}
	first, err := s.db.RecordTurnResult(tc.Folder, turnID, req.SessionID, req.Status)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if first {
		if req.SessionID != "" {
			_ = s.db.PutSession(tc.Folder, tc.Topic, req.SessionID)
		}
		for model, c := range req.Models {
			_ = s.db.PutCost(tc.Folder, turnID, model, c.Input, c.Output, c.CostCents)
		}
		_ = s.db.SetTurnState(turnID, "done")
		if s.loop != nil {
			s.loop.publishRoundDone(trimWeb(tc.ChatJID), turnID)
		}
	}
	writeJSON(w, 200, apiv1.TurnResultAck{Recorded: first})
}

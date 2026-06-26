package routd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
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
		s.deliverStatus(tc, jid, st)
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
		_ = s.db.SetEngagement(tc.ChatJID, tc.Topic, tc.Folder, s.engagementT)
	}
	s.deliverTurn(tc, jid, &row, threaded)
	if s.deliver != nil {
		_ = s.deliver.Typing(jid, false)
	}
	return 200, apiv1.SendResult{MessageID: row.ID, PlatformID: row.PlatformID, Status: row.Status}, &row
}

// replyThreadRoot resolves the trigger message id a REPLY should root a new
// platform thread on, or "" when it must not start one: the turn is already in
// a thread (topic set), the delivery is redirected off the trigger chat
// (delegation — the trigger id is not a platform id there), no trigger id is
// recorded, or the group's thread_replies preference resolves off. The
// preference defaults to the chat's multi-user flag: group chats thread, DMs
// stay inline. send (threaded=false) never calls this — it posts top-level.
func (s *Server) replyThreadRoot(tc TurnContext, jid string) string {
	if tc.Topic != "" || tc.TriggerMsgID == "" || jid != tc.ChatJID {
		return ""
	}
	if !s.db.ThreadReplies(tc.Folder, s.db.ChatIsGroup(jid)) {
		return ""
	}
	return tc.TriggerMsgID
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

// deliverTurn sends row via the platform, computing threadRoot from tc when
// threaded=true. Use for reply (threaded=true) and status (threaded=true);
// send (threaded=false). Callers never supply threadRoot directly — this is the
// single policy site that calls replyThreadRoot.
func (s *Server) deliverTurn(tc TurnContext, jid string, row *core.Message, threaded bool) {
	threadRoot, threadID := "", tc.Topic
	if threaded {
		threadRoot = s.replyThreadRoot(tc, jid)
	}
	s.deliverRow(tc, jid, row, threadRoot, threadID)
}

// deliverStatus is the ONE renderer for an interim "⏳ ..." progress notice:
// build an out-of-band row, deliver it, persist it. Always threaded so it
// appears alongside the final reply. Shared by appendAndDeliver's status loop
// and the mid-turn submit_status MCP path so the two can't drift.
func (s *Server) deliverStatus(tc TurnContext, jid, text string) {
	row := s.outboundRow(tc, jid, "⏳ "+text, "")
	s.deliverTurn(tc, jid, &row, true)
	_ = s.db.PutMessage(row)
}

// deliverRow attempts the platform send and stamps platform_id+sent on success.
// threadRoot is non-empty only for a REPLY that should start a new platform
// thread on the trigger message (replyThreadRoot). threadID follows an
// existing platform thread (Slack thread_ts, Discord thread channel ID).
// A SEND_DISABLED_GROUPS folder skips the send entirely but still lands the
// row status=sent so the poll loop never retries it.
func (s *Server) deliverRow(tc TurnContext, jid string, row *core.Message, threadRoot, threadID string) {
	if s.mutedGroup(tc.Folder) {
		row.Status = core.MessageStatusSent
		return
	}
	if s.deliver == nil {
		return
	}
	if pid, err := s.deliver.Send(jid, row.Content, row.ReplyToID, threadID, threadRoot, row.ID); err == nil {
		row.PlatformID = pid
		row.Status = core.MessageStatusSent
		// A reply that started a new thread (threadRoot set) is now IN that thread.
		// Record its topic so threadHasBotMessage finds it when subsequent messages
		// in the thread arrive. The thread ID = threadRoot on both Discord (thread
		// channel ID = root message ID) and Slack (thread_ts = root message ts).
		if threadRoot != "" && row.Topic == "" {
			row.Topic = threadRoot
		}
	} else {
		slog.Error("deliver send failed", "jid", jid, "folder", tc.Folder, "err", err)
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
			if pid, err := s.deliver.Document(jid, req.Path, req.Name, req.Caption, req.ReplyToID, tc.Topic, msgID); err == nil {
				row.PlatformID = pid
				row.Status = core.MessageStatusSent
			}
		}
		return 200, apiv1.SendResult{MessageID: msgID, PlatformID: row.PlatformID, Status: row.Status}, &row
	})
}

// handlePost/Forward/Quote/Repost/SendVoice are the REST twins of the
// social/feed MCP tools (mcp.go buildGatedFns). Each hits the SAME Deliverer
// method (or s.mcpSendVoice) the in-process MCP closure does — one renderer,
// many sinks (CLAUDE.md). post/forward/quote/repost are pure relays: no routd
// messages row (matching the MCP closures, whose recordOutbound layer keys on
// the returned platform id), returning SendResult{PlatformID, sent}. They use
// idem(required=false) so a retry dedups. returnTarget redirects a delegated
// turn's jid to its origin chat, exactly as handleDocument does.

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/post", false, func(body []byte) (int, any, *core.Message) {
		var req apiv1.PostRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid required"}, nil
		}
		tc, status, errResp := s.openTurn(turnID)
		if errResp != nil {
			return status, errResp, nil
		}
		if s.deliver == nil {
			return relay("", nil)
		}
		return relay(s.deliver.Post(returnTarget(tc, req.JID), req.Content, req.MediaPaths))
	})
}

func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/forward", false, func(body []byte) (int, any, *core.Message) {
		var req apiv1.ForwardRequest
		if json.Unmarshal(body, &req) != nil || req.SourceMsgID == "" || req.TargetJID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "source_msg_id and target_jid required"}, nil
		}
		tc, status, errResp := s.openTurn(turnID)
		if errResp != nil {
			return status, errResp, nil
		}
		if s.deliver == nil {
			return relay("", nil)
		}
		return relay(s.deliver.Forward(req.SourceMsgID, returnTarget(tc, req.TargetJID), req.Comment))
	})
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/quote", false, func(body []byte) (int, any, *core.Message) {
		var req apiv1.QuoteRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" || req.SourceMsgID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid and source_msg_id required"}, nil
		}
		tc, status, errResp := s.openTurn(turnID)
		if errResp != nil {
			return status, errResp, nil
		}
		if s.deliver == nil {
			return relay("", nil)
		}
		return relay(s.deliver.Quote(returnTarget(tc, req.JID), req.SourceMsgID, req.Comment))
	})
}

func (s *Server) handleRepost(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/repost", false, func(body []byte) (int, any, *core.Message) {
		var req apiv1.RepostRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" || req.SourceMsgID == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid and source_msg_id required"}, nil
		}
		tc, status, errResp := s.openTurn(turnID)
		if errResp != nil {
			return status, errResp, nil
		}
		if s.deliver == nil {
			return relay("", nil)
		}
		return relay(s.deliver.Repost(returnTarget(tc, req.JID), req.SourceMsgID))
	})
}

func (s *Server) handleSendVoice(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turn_id")
	if !s.authzTurn(w, r, turnID, scopeSend...) {
		return
	}
	s.idem(w, r, "POST /v1/turns/send_voice", false, func(body []byte) (int, any, *core.Message) {
		var req apiv1.VoiceRequest
		if json.Unmarshal(body, &req) != nil || req.JID == "" || req.Text == "" {
			return 400, apiv1.Err{Error: "bad_request", Message: "jid and text required"}, nil
		}
		tc, status, errResp := s.openTurn(turnID)
		if errResp != nil {
			return status, errResp, nil
		}
		// mcpSendVoice re-applies returnTarget + thread default internally; pass
		// tc.Folder for the synthesis config lookup, mirroring the MCP closure.
		return relay(s.mcpSendVoice(turnID, req.JID, req.Text, req.Voice, tc.Folder, req.ThreadID))
	})
}

// openTurn fetches the turn context and rejects a closed turn — the guard the
// relay handlers run before touching the Deliverer. (0, nil) errResp on success.
func (s *Server) openTurn(turnID string) (TurnContext, int, any) {
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return tc, 409, apiv1.Err{Error: "unknown_turn", Message: "no turn context for turn_id"}
	}
	if s.callbackClosed(tc) {
		return tc, 409, apiv1.Err{Error: "turn_done", Message: "turn already terminal"}
	}
	return tc, 0, nil
}

// relay shapes a Deliverer social-verb call (platform id + err) into the idem
// exec triple: 422 unsupported on an adapter error, else 200 SendResult{sent}.
// No messages row (the social verbs are deliver-only, matching the MCP closures).
func relay(pid string, err error) (int, any, *core.Message) {
	if err != nil {
		return 422, apiv1.Err{Error: "unsupported", Message: err.Error()}, nil
	}
	return 200, apiv1.SendResult{PlatformID: pid, Status: core.MessageStatusSent}, nil
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
		if req.TimedOut {
			slog.Warn("agent turn timed out; summary delivered",
				"folder", tc.Folder, "turn_id", turnID)
		}
		// Deliver the agent's prose result as the reply when it called no
		// reply/send tool this turn. The agent's contract (ant/CLAUDE.md: "text
		// outside <think> is delivered") relies on submit_turn's result reaching
		// the user; the split had dropped it, so a normal Q&A turn produced output
		// that never left routd (krons no-reply outage 2026-06-08). Same
		// strip-think/format/deliver path as an explicit reply; skipped when the
		// agent already delivered a row (no double-send), and BEFORE marking the
		// turn done so the callback isn't yet closed. Isolated cron turns
		// (compact-memories etc.) are silent by design — their output is the
		// file write + audit log, never a chat message — so never deliver their
		// result prose regardless of what the agent left outside <think>.
		isolated := strings.HasPrefix(tc.Trigger, "timed-isolated:")
		if req.Result != "" && !isolated && !s.db.TurnHasBotReply(turnID) {
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
			obs.RecordModelTokens(model, tc.Folder, "in", c.Input)
			obs.RecordModelTokens(model, tc.Folder, "out", c.Output)
		}
		_ = s.db.SetTurnState(turnID, "done")
		if s.loop != nil {
			s.loop.publishRoundDone(strings.TrimPrefix(tc.ChatJID, "web:"), turnID)
		}
	}
	return first, nil
}

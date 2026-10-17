package routd

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
)

// The turn's outbound lifecycle: the prose result submit_turn carries and the
// <status> blocks, rendered into messages rows and delivered through the
// Deliverer. The agent's tool calls ride the in-process MCP socket (mcp.go).

// turnLocks serializes append-and-deliver per turn_id so out-of-order arrivals
// append in receive order.
var turnLocks sync.Map // turn_id -> *sync.Mutex

func lockTurn(turnID string) func() {
	m, _ := turnLocks.LoadOrStore(turnID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// callbackClosed reports whether a turn's outbound surface is closed: only
// AFTER POST /v1/runs returns, so a live run's trailing frames stay valid past
// an early submit_turn that flipped state→done.
func (s *Server) callbackClosed(tc TurnContext) bool { return tc.RunReturned }

// returnTarget redirects a turn's delivery to the delegation return address
// (the trigger batch's forwarded_from) when set.
func returnTarget(tc TurnContext, jid string) string {
	if tc.ReturnTo != "" {
		return tc.ReturnTo
	}
	return jid
}

// appendAndDeliver renders the agent's raw output and appends + delivers it as
// the turn's threaded reply: strip <think>, hand each <status> to the progress
// renderer, FormatOutbound the rest. A pure-think output produces no row. A
// turn whose run already returned delivers nothing. A failed progress update is
// logged and the reply goes out regardless: the reply is the product.
func (s *Server) appendAndDeliver(turnID, jid, text string) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		slog.Warn("appendAndDeliver: no turn context; output dropped", "turn_id", turnID)
		return
	}
	if s.callbackClosed(tc) {
		slog.Warn("appendAndDeliver: run already returned; late output dropped",
			"folder", tc.Folder, "turn_id", turnID)
		return
	}
	jid = returnTarget(tc, jid)

	stripped, statuses := router.ExtractStatusBlocks(router.StripThinkBlocks(text))
	for _, st := range statuses {
		if err := s.updateProgress(tc, jid, st); err != nil {
			slog.Error("appendAndDeliver: progress update failed; reply continues",
				"folder", tc.Folder, "turn_id", turnID, "err", err)
		}
	}

	clean := router.FormatOutbound(stripped)
	if clean == "" {
		// Pure <think> / silent refusal.
		return
	}

	row := s.outboundRow(tc, jid, clean, s.db.LastReplyID(jid, tc.Topic))
	_ = s.db.SetLastReply(jid, tc.Topic, row.ID, tc.Folder)
	if s.engagementT > 0 {
		until := time.Now().Add(s.engagementT)
		if err := s.db.store().RenewEngagement(turnID, tc.ChatJID, tc.Topic, tc.Folder, until); err != nil {
			slog.Error("renew reply engagement", "turn_id", turnID, "err", err)
		}
	}
	status := s.deliverTurn(tc, jid, &row)
	if err := s.db.PutOutbound(row, status); err != nil {
		slog.Error("appendAndDeliver: PutOutbound failed; reply was delivered but not persisted, will be missing from history/audit",
			"folder", tc.Folder, "turn_id", turnID, "err", err)
	}
}

// replyThreadRoot resolves the trigger id a REPLY roots a new platform thread
// on, or "" when it must not: the turn is already in a thread, the delivery is
// redirected off the trigger chat, no trigger id is recorded, or the group's
// thread_replies preference (default: the chat's multi-user flag) is off. send
// never calls this — it posts top-level.
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
		TurnID: tc.TurnID,
	}
}

// deliverTurn sends row as a threaded reply: threadRoot from replyThreadRoot,
// threadID the turn's topic. The top-level `send` verb never comes through
// here (mcp.go mcpDeliver).
func (s *Server) deliverTurn(tc TurnContext, jid string, row *core.Message) string {
	return s.deliverRow(tc, jid, row, s.replyThreadRoot(tc, jid), tc.Topic)
}

// deliverRow attempts the send, stamps platform_id on success, and ANSWERS the
// status the caller stores the row under: `sent` is the absence of a delivery
// item, so the status decides whether an item is written at all
// (store/message_delivery.go). threadRoot is non-empty only for a REPLY that
// starts a new platform thread. A SEND_DISABLED_GROUPS folder skips the send
// but lands `sent`, so the retry sweep never sees it.
func (s *Server) deliverRow(tc TurnContext, jid string, row *core.Message,
	threadRoot, threadID string) string {
	if s.mutedGroup(tc.Folder) {
		return core.MessageStatusSent
	}
	// A bare-folder jid has no channel (a system/auto turn or a mis-targeted
	// delegation): a doomed Send tripped the per-folder breaker en masse on
	// every MIGRATION_VERSION bump. The row lands `failed`, NOT `sent`: `failed`
	// IS the dead-letter state with delivery_attempts=0, so the content stays
	// visible in dead_letters instead of recording a delivery that never
	// happened (BUGS L76).
	if !strings.Contains(jid, ":") {
		slog.Warn("no channel for this jid; the message is dead-lettered, not delivered",
			"jid", jid, "folder", tc.Folder, "msg_id", row.ID)
		return core.MessageStatusFailed
	}
	if s.deliver == nil {
		return core.MessageStatusPending
	}
	// STAMPED BEFORE THE SEND: a failed send returns `pending` and the row is
	// stored as it stands, so a row stamped after the error has no topic and
	// the outbound sweep re-sends it TOP-LEVEL (BUGS L169). The thread ID IS
	// threadRoot on Discord and Slack, so the root holds whether or not this
	// send lands.
	if threadRoot != "" && row.Topic == "" {
		row.Topic = threadRoot
	}
	pid, err := s.deliver.Send(jid, row.Content, row.ReplyToID, threadID, threadRoot, row.ID)
	if err != nil {
		slog.Error("deliver send failed", "jid", jid, "folder", tc.Folder, "err", err)
		return core.MessageStatusPending
	}
	row.PlatformID = pid
	return core.MessageStatusSent
}

// gateway is the model gateway's client, or nil when the instance is not
// metered. The Loop owns it because the mint brackets a dispatch; one
// declaration, so no surface decides differently whether this instance is
// metered.
func (s *Server) gateway() *modelGateway {
	if s.loop == nil {
		return nil
	}
	return s.loop.gateway
}

// logSelfReportedCost records what the agent says the turn cost — a log line
// and never a row (spec 6/39): with a gateway the meter holds the number and a
// copy could only disagree; without one, a number the spender chose is not one
// a cap can be enforced on. A self-report that disagrees with the meter is the
// shape a bypassed gateway takes.
func logSelfReportedCost(metered bool, folder, turnID string, models map[string]apiv1.ModelCost) {
	for model, c := range models {
		slog.Info("agent self-reported model usage",
			"folder", folder, "turn_id", turnID, "model", model,
			"input", c.Input, "output", c.Output, "cost_cents", c.CostCents,
			"metered", metered)
		obs.RecordModelTokens(model, folder, "in", c.Input)
		obs.RecordModelTokens(model, folder, "out", c.Output)
	}
}

// recordTurnResult is the turn-completion writer behind submit_turn. It
// records the outcome idempotently and, on the FIRST record, persists
// session_id, flips the turn to done and publishes round_done.
func (s *Server) recordTurnResult(turnID string, req apiv1.TurnResult) (bool, error) {
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return false, fmt.Errorf("no turn context for turn_id")
	}
	first, err := s.db.RecordTurnResult(tc.Folder, turnID, req.SessionID, req.Status, req.Error)
	if err != nil {
		return false, err
	}
	if first {
		if req.TimedOut {
			slog.Warn("agent turn timed out; summary delivered",
				"folder", tc.Folder, "turn_id", turnID)
		}
		// The prose result is the reply when the agent called no reply/send tool
		// this turn: the contract is "text outside <think> is delivered" (krons
		// no-reply outage 2026-06-08). Done BEFORE marking the turn done.
		// Isolated cron turns are silent by design — their output is the file
		// write, never a chat message.
		isolated := strings.HasPrefix(tc.Trigger, "timed-isolated:")
		if req.Result != "" && !isolated && !s.db.TurnHasBotReply(tc.ChatJID, turnID) {
			// Counted: this branch decides whether spec 6/44's inversion is cheap
			// (BUGS L329).
			obs.RecordProseFallbackTurn(tc.Folder)
			s.appendAndDeliver(turnID, tc.ChatJID, req.Result)
		}
		// An isolated turn holds no session lineage; writing its id back would
		// hand the chat a throwaway transcript the cron ran in (BUGS L54).
		if req.SessionID != "" && !isolated {
			if err := s.db.PutSession(tc.Folder, tc.Topic, req.SessionID); err != nil {
				slog.Error("recordTurnResult: PutSession failed; next turn in this topic loses session continuity",
					"folder", tc.Folder, "turn_id", turnID, "err", err)
			}
		}
		logSelfReportedCost(s.gateway() != nil, tc.Folder, turnID, req.Models)
		finishTurn(s.db, s.deliver, turnID, endDone)
		if s.loop != nil {
			// VERBATIM — webd's hub keys on it as-is; a TrimPrefix here hung
			// every suffixed hook: widget (BUGS L30).
			s.loop.publishRoundDone(tc.ChatJID, turnID)
		}
	}
	return first, nil
}

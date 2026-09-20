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

// The turn's outbound lifecycle. The agent's tool calls (reply/send/like/…)
// ride the in-process MCP socket (mcp.go, ipc); what lands here is the rest of
// a turn's output — the prose result submit_turn carries and the <status>
// blocks — rendered into messages rows and delivered through the Deliverer.

// turnLocks serializes append-and-deliver per turn_id so out-of-order arrivals
// append in receive order.
var turnLocks sync.Map // turn_id -> *sync.Mutex

func lockTurn(turnID string) func() {
	m, _ := turnLocks.LoadOrStore(turnID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// callbackClosed reports whether a turn's outbound surface is closed: the
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

// appendAndDeliver renders the agent's raw output then appends + delivers it as
// the turn's threaded reply: strip <think>, surface each <status> as a separate
// progress message, then FormatOutbound the remainder. A pure-think output
// produces no reply row. SEND_DISABLED_GROUPS folders persist status=sent but
// skip the platform send. A turn whose run already returned delivers nothing:
// the output has no live run to belong to.
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
	// Interim progress notices: delivered + persisted out-of-band, never the
	// reply row.
	for _, st := range statuses {
		s.deliverStatus(tc, jid, st)
	}

	clean := router.FormatOutbound(stripped)
	if clean == "" {
		// Pure <think> / silent refusal: nothing user-visible to send.
		return
	}

	row := s.outboundRow(tc, jid, clean, s.db.LastReplyID(jid, tc.Topic))
	_ = s.db.SetLastReply(jid, tc.Topic, row.ID, tc.Folder)
	if !strings.HasPrefix(tc.Trigger, "timed-") {
		_ = s.db.SetEngagement(tc.ChatJID, tc.Topic, tc.Folder, s.engagementT)
	}
	status := s.deliverTurn(tc, jid, &row)
	if s.deliver != nil {
		_ = s.deliver.Typing(jid, false)
	}
	if err := s.db.PutOutbound(row, status); err != nil {
		slog.Error("appendAndDeliver: PutOutbound failed; reply was delivered but not persisted, will be missing from history/audit",
			"folder", tc.Folder, "turn_id", turnID, "err", err)
	}
}

// replyThreadRoot resolves the trigger message id a REPLY should root a new platform
// thread on, or "" when it must not start one: the turn is already in a thread, the
// delivery is redirected off the trigger chat, no trigger id is recorded, or the
// group's thread_replies preference resolves off. That preference defaults to the
// chat's multi-user flag. send never calls this — it posts top-level.
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

// deliverTurn sends row via the platform as a threaded reply: threadRoot from
// replyThreadRoot — the single policy site that calls it — and threadID the
// turn's topic. Both the reply row and a status row are threaded; the
// top-level `send` verb never comes through here (mcp.go mcpDeliver).
func (s *Server) deliverTurn(tc TurnContext, jid string, row *core.Message) string {
	return s.deliverRow(tc, jid, row, s.replyThreadRoot(tc, jid), tc.Topic)
}

// deliverStatus is the ONE renderer for an interim progress notice: build an
// out-of-band row, deliver it, persist it. Always threaded, and shared by
// appendAndDeliver's status loop and the mid-turn submit_status path so the two
// cannot drift. The row is stamped verb="status" so it is excluded from
// TurnHasBotReply and is the row submit_status edits in place (spec 5/24).
func (s *Server) deliverStatus(tc TurnContext, jid, text string) {
	row := s.outboundRow(tc, jid, "⏳ "+text, "")
	row.Verb = "status"
	status := s.deliverTurn(tc, jid, &row)
	_ = s.db.PutOutbound(row, status)
}

// deliverRow attempts the platform send, stamps platform_id on success, and
// ANSWERS the delivery status the caller must store the row under.
//
// The status is returned rather than set on the row, because a message does not
// carry one: `sent` is the absence of a delivery item, and which of the three the
// row takes is what decides whether an item is written at all
// (store/message_delivery.go).
//
// threadRoot is non-empty only for a REPLY that should start a new platform
// thread; threadID follows an existing one. A SEND_DISABLED_GROUPS folder skips
// the send but still lands the row `sent`, so the retry sweep never sees it.
func (s *Server) deliverRow(tc TurnContext, jid string, row *core.Message,
	threadRoot, threadID string) string {
	if s.mutedGroup(tc.Folder) {
		return core.MessageStatusSent
	}
	// A bare-folder jid has no channel to deliver to — a system/auto turn or a
	// mis-targeted delegation whose prose is not a user reply. Skip the Send: an
	// ERROR-spammed doomed Send tripped the per-folder breaker en masse on every
	// MIGRATION_VERSION bump.
	//
	// The row lands `failed`, NOT `sent`. `failed` IS the dead-letter state and
	// delivery_attempts=0 already means "refused permanently", so the content stays
	// visible in the dead_letters surface instead of recording a delivery that never
	// happened. Two operator alerts were lost to the `sent` stamp (BUGS L76).
	if !strings.Contains(jid, ":") {
		slog.Warn("no channel for this jid; the message is dead-lettered, not delivered",
			"jid", jid, "folder", tc.Folder, "msg_id", row.ID)
		return core.MessageStatusFailed
	}
	if s.deliver == nil {
		return core.MessageStatusPending
	}
	// A reply aimed at a thread belongs to that thread from the moment this
	// function decides to send it there, not from the moment the platform
	// accepts it. The thread ID = threadRoot on both Discord (thread channel ID
	// = root message ID) and Slack (thread_ts = root message ts), so the root
	// exists whether or not this send lands.
	//
	// STAMPED BEFORE THE SEND, because a failed send returns `pending` and
	// `PutOutbound` stores the row as it stands. Stamping after the error return
	// left the pending row with no topic, and the outbound sweep
	// (`routd/loop.go`) sends `threadRoot=""` — so the retry posted the reply
	// TOP-LEVEL, outside the thread it was written for (BUGS L169).
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

// gateway is the model gateway's client, or nil on an instance that names none
// and therefore has no meter. The Loop owns it because the mint brackets a
// dispatch; this is the same declaration read from the HTTP and callback sides,
// so no surface can decide differently whether this instance is metered.
func (s *Server) gateway() *modelGateway {
	if s.loop == nil {
		return nil
	}
	return s.loop.gateway
}

// logSelfReportedCost records what the agent says the turn cost. It is a log
// line and never a row (spec 6/39): with a gateway, the meter already holds the
// number and a copy could only disagree with it; with no gateway there is no
// meter, and a number the spender chose is not one a cap could be enforced on.
// It stays in the log because a self-report that disagrees with the meter is the
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

// recordTurnResult is the turn-completion writer behind the in-process
// submit_turn MCP method. It records the outcome idempotently, and on the FIRST
// record persists session_id + per-model cost, flips the turn to done, and
// publishes round_done. Returns whether this was the first record.
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
		// Deliver the agent's prose result as the reply when it called no reply/send
		// tool this turn. The agent's contract is "text outside <think> is delivered",
		// and the split had dropped it, so a normal Q&A turn produced output that never
		// left routd (krons no-reply outage 2026-06-08). Skipped when the agent already
		// delivered a row, and done BEFORE marking the turn done. Isolated cron turns are
		// silent by design — their output is the file write, never a chat message.
		isolated := strings.HasPrefix(tc.Trigger, "timed-isolated:")
		if req.Result != "" && !isolated && !s.db.TurnHasBotReply(tc.ChatJID, turnID) {
			// Counted, because this branch decides whether spec 6/44's inversion
			// is cheap: requiring an explicit reply tool costs one extra model
			// round trip on every turn that lands here (BUGS L329).
			obs.RecordProseFallbackTurn(tc.Folder)
			s.appendAndDeliver(turnID, tc.ChatJID, req.Result)
		}
		// An isolated turn holds no session lineage — it neither resumes the
		// topic's transcript nor becomes it. Writing its id back would hand the
		// chat a throwaway transcript the cron ran in, which is the same context
		// loss from the other direction (BUGS L54).
		if req.SessionID != "" && !isolated {
			if err := s.db.PutSession(tc.Folder, tc.Topic, req.SessionID); err != nil {
				slog.Error("recordTurnResult: PutSession failed; next turn in this topic loses session continuity",
					"folder", tc.Folder, "turn_id", turnID, "err", err)
			}
		}
		logSelfReportedCost(s.gateway() != nil, tc.Folder, turnID, req.Models)
		if err := s.db.SetTurnState(turnID, "done"); err != nil {
			slog.Error("recordTurnResult: SetTurnState(done) failed; turn may appear stuck in-progress",
				"folder", tc.Folder, "turn_id", turnID, "err", err)
		}
		if s.loop != nil {
			// The chat JID travels VERBATIM — webd's hub keys on it as-is. A
			// TrimPrefix here left hook: chats keyed differently on each end
			// and hung every suffixed widget (BUGS L30).
			s.loop.publishRoundDone(tc.ChatJID, turnID)
		}
	}
	return first, nil
}

package routd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/obs"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// The turn's outbound lifecycle: explicit delivery tools and progress updates.

// turnLocks serializes progress, completion and nudge decisions per turn.
var turnLocks sync.Map // turn_id -> *sync.Mutex

func lockTurn(turnID string) func() {
	m, _ := turnLocks.LoadOrStore(turnID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// turnNudge is the one renderer shared by Stop hooks and turn submission.
func turnNudge(tc TurnContext, statusRow bool) string {
	if tc.Addressed {
		return "[deliver] You were addressed and delivered nothing. Call reply now (a like counts)."
	}
	if statusRow {
		return "[deliver] You posted a status and delivered nothing. Call reply with the answer."
	}
	return ""
}

func (s *Server) turnDelivered(tc TurnContext) (bool, error) {
	if s.db.TurnHasBotReply(tc.ChatJID, tc.TurnID) {
		return true, nil
	}
	return audit.HasSuccessfulTurnAction(context.Background(), s.db.Obj(), tc.TurnID, tc.Folder, tc.StartedAt,
		"message.like", "message.dislike", "message.edit", "message.delete", "message.pin", "message.unpin")
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

// silentTurnNotice closes an owed turn that remained silent after its one nudge,
// or a clean run that failed to submit any result at all.
const silentTurnNotice = "⚠️ I couldn't produce a reply to that — please re-send."

const timedOutNotice = "⚠️ I ran out of time on that — say 'continue' to resume, or narrow the request."

// recordTurnResult decides whether a successful submission may close the turn
// before recording it. A nudge holds the same turn open for one continuation.
func (s *Server) recordTurnResult(turnID string, req apiv1.TurnResult) (ipc.TurnAck, error) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return ipc.TurnAck{}, fmt.Errorf("no turn context for turn_id")
	}
	var delivered bool
	if req.Status == "success" {
		var err error
		delivered, err = s.turnDelivered(tc)
		if err != nil {
			return ipc.TurnAck{}, err
		}
	}
	end := endDone
	if req.Status == "error" {
		end = endFailed
	}
	notice := ""
	if req.Status == "success" && !s.db.TurnResultRecorded(tc.Folder, turnID) {
		if req.TimedOut && !delivered {
			notice = timedOutNotice
			end = endFailed
		} else if !delivered {
			check, err := s.checkTurn(tc)
			if err != nil {
				return ipc.TurnAck{}, err
			}
			if check.Nudge != "" {
				return ipc.TurnAck{OK: true, Nudge: check.Nudge}, nil
			}
			if check.Owed {
				notice = silentTurnNotice
				end = endFailed
			} else {
				slog.Info("turn completed deliberately silent", "folder", tc.Folder, "turn_id", turnID)
			}
		}
	}
	first, err := s.db.RecordTurnResult(tc.Folder, turnID, req.SessionID, req.Status, req.Error)
	if err != nil {
		return ipc.TurnAck{}, err
	}
	if first {
		if req.TimedOut {
			slog.Warn("agent turn timed out", "folder", tc.Folder, "turn_id", turnID)
		}
		if tc.Nudged > 0 {
			outcome := "silent"
			if delivered {
				outcome = "delivered"
			}
			obs.RecordNudgedTurn(tc.Folder, outcome)
		}
		if notice != "" && s.deliver != nil {
			jid := returnTarget(tc, tc.ChatJID)
			if _, err := s.deliver.Send(jid, notice, "", tc.Topic, "", "silent-"+turnID); err != nil {
				slog.Error("turn completion notice failed", "folder", tc.Folder, "turn_id", turnID, "err", err)
			}
		}
		// Isolated turns never replace the chat's session lineage (BUGS L54).
		if req.SessionID != "" && !strings.HasPrefix(tc.Trigger, "timed-isolated:") {
			if err := s.db.PutSession(tc.Folder, tc.Topic, req.SessionID); err != nil {
				slog.Error("recordTurnResult: PutSession failed; next turn in this topic loses session continuity", "folder", tc.Folder, "turn_id", turnID, "err", err)
			}
		}
		logSelfReportedCost(s.gateway() != nil, tc.Folder, turnID, req.Models)
		finishTurnLocked(s.db, s.deliver, turnID, end)
		if s.loop != nil {
			s.loop.publishRoundDone(tc.ChatJID, turnID)
		}
	}
	return ipc.TurnAck{OK: true}, nil
}

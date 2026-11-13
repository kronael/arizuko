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
	"github.com/kronael/arizuko/store"
)

// The turn's outbound lifecycle: explicit delivery tools and progress updates.

// turnLocks serializes progress, completion and nudge decisions per turn.
var turnLocks = struct {
	sync.Mutex
	entries map[string]*turnLock
}{entries: make(map[string]*turnLock)}

type turnLock struct {
	sync.Mutex
	refs int
}

func lockTurn(turnID string) func() {
	turnLocks.Lock()
	lock := turnLocks.entries[turnID]
	if lock == nil {
		lock = &turnLock{}
		turnLocks.entries[turnID] = lock
	}
	lock.refs++
	turnLocks.Unlock()
	lock.Lock()
	return func() {
		turnLocks.Lock()
		lock.Unlock()
		lock.refs--
		if lock.refs == 0 {
			delete(turnLocks.entries, turnID)
		}
		turnLocks.Unlock()
	}
}

// deliverNudge is the one nudge text, shared by Stop hooks and turn submission.
const deliverNudge = "[deliver] You were addressed and delivered nothing. Call reply now (a like counts)."

func (s *Server) turnDelivered(tc TurnContext) (bool, error) {
	target := returnTarget(tc, tc.ChatJID)
	if s.db.TurnHasBotReply(target, tc.TurnID) {
		return true, nil
	}
	if tj := discordThreadJID(target, tc.Topic); tj != "" && s.db.TurnHasBotReply(tj, tc.TurnID) {
		return true, nil
	}
	return audit.HasSuccessfulTurnAction(context.Background(), s.db.Obj(), tc.TurnID, tc.Folder, tc.StartedAt,
		"message.like", "message.dislike", "message.edit", "message.delete", "message.pin", "message.unpin")
}

// discordThreadJID is the thread's own JID for a Discord turn whose topic is a
// thread id: discd places a thread under its parent channel's JID with the
// thread id as topic, while the agent replies to `discord:<guild>/<thread>`
// (discd/bot.go chatAndTopic). Empty for any other turn.
func discordThreadJID(jid, topic string) string {
	guild, _, ok := strings.Cut(jid, "/")
	if !ok || topic == "" || !strings.HasPrefix(guild, "discord:") {
		return ""
	}
	return guild + "/" + topic
}

func (s *Server) callbackClosed(tc TurnContext) bool {
	return tc.RunReturned || tc.State == store.TurnHeld ||
		(tc.AttemptID != "" && tc.ProviderFailureAttempt == tc.AttemptID)
}

// returnTarget redirects a turn's delivery to the delegation return address
// (the trigger batch's forwarded_from) when set.
func returnTarget(tc TurnContext, jid string) string {
	if tc.ReturnTo != "" {
		return tc.ReturnTo
	}
	return jid
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
	// delegation). The row lands `failed`, NOT `sent`: `failed`
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

// recordTurnResultAttempt decides whether a successful submission may close the turn
// before recording it. A nudge holds the same turn open for one continuation.
func (s *Server) recordTurnResultAttempt(turnID, attempt string, req apiv1.TurnResult) (ipc.TurnAck, error) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return ipc.TurnAck{}, fmt.Errorf("no turn context for turn_id")
	}
	if s.callbackClosed(tc) || (attempt != "" && tc.AttemptID != attempt) {
		return ipc.TurnAck{}, fmt.Errorf("turn attempt is closed")
	}
	if req.ProviderFailure != nil {
		if req.Status != "error" {
			return ipc.TurnAck{}, fmt.Errorf("provider failure requires an error result")
		}
		reset, _ := time.Parse(time.RFC3339Nano, req.ProviderFailure.ResetAt)
		if s.loop == nil {
			return ipc.TurnAck{}, fmt.Errorf("turn controller unavailable")
		}
		_, err := s.loop.q.refuse(s.loop, tc, req.ProviderFailure.Reason, reset)
		if err != nil {
			return ipc.TurnAck{}, err
		}
		s.loop.pauseProgress(returnTarget(tc, tc.ChatJID), tc.RunID)
		if err := s.loop.publishOwedStatusLocked(tc); err != nil {
			return ipc.TurnAck{}, err
		}
		return ipc.TurnAck{OK: true}, nil
	}
	if s.db.TurnResultRecorded(tc.Folder, turnID) {
		return ipc.TurnAck{OK: true}, nil
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
	var notice *turnStatus
	if req.Status == "refused" {
		slog.Warn("agent turn refused", "folder", tc.Folder, "turn_id", turnID, "err", req.Error)
		notice = &turnStatus{phase: "final", cause: "The model refused this request (provider safety filter)"}
		end = endFailed
	}
	if req.Status == "success" {
		if req.TimedOut {
			notice = &turnStatus{phase: "timeout"}
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
				notice = &turnStatus{phase: "silent"}
				end = endFailed
			} else {
				slog.Info("turn completed deliberately silent", "folder", tc.Folder, "turn_id", turnID)
			}
		}
	}
	if end == endFailed && notice == nil {
		notice = &turnStatus{phase: "final", cause: "Agent could not complete the request"}
	}
	if notice != nil {
		if err := applyStatus(s.db, s.deliver, tc, returnTarget(tc, tc.ChatJID), *notice); err != nil {
			return ipc.TurnAck{}, err
		}
	}
	status, failure := req.Status, req.Error
	if notice != nil {
		status, failure = "error", renderTurnStatus(*notice)
	}
	usage, err := turnUsage(tc.Folder, turnID, req.Harness, s.gateway() != nil, req.Models)
	if err != nil {
		return ipc.TurnAck{}, err
	}
	rows, err := usageRows(usage)
	if err != nil {
		return ipc.TurnAck{}, err
	}
	first, err := s.db.RecordTurnResult(tc.Folder, turnID, req.SessionID, status, failure, usage)
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

		// Isolated turns never replace the chat's session lineage (BUGS L54).
		if req.SessionID != "" && !strings.HasPrefix(tc.Trigger, core.IsolatedSenderPrefix) {
			if err := s.db.PutSession(tc.Folder, tc.Topic, req.SessionID); err != nil {
				slog.Error("recordTurnResult: PutSession failed; next turn in this topic loses session continuity", "folder", tc.Folder, "turn_id", turnID, "err", err)
			}
		}
		logTurnUsage(rows)
		finishTurnLocked(s.db, s.deliver, turnID, end)
		if s.loop != nil {
			s.loop.publishRoundDone(tc.ChatJID, turnID)
		}
	}
	return ipc.TurnAck{OK: true}, nil
}

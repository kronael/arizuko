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
	return tc.State == store.TurnWaitingCapacity || tc.RunReturned || (tc.AttemptID != "" && tc.ProviderFailureAttempt == tc.AttemptID)
}

// returnTarget redirects a turn's delivery to the delegation return address
// (the trigger batch's forwarded_from) when set.
func returnTarget(tc TurnContext, jid string) string {
	if tc.ReturnTo != "" {
		return tc.ReturnTo
	}
	return jid
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
		s.loop.pauseProgress(returnTarget(tc, tc.ChatJID), tc.Topic, tc.RunID)
		if err := s.loop.publishOwedStatusLocked(tc); err != nil {
			return ipc.TurnAck{}, err
		}
		return ipc.TurnAck{OK: true}, nil
	}
	if s.db.TurnResultRecorded(tc.Folder, turnID) {
		return ipc.TurnAck{OK: true}, nil
	}
	var delivered bool
	continuing := s.loop != nil && (req.TimedOut || req.Checkpoint && req.Status == "success")
	if req.Status == "success" && !continuing {
		var err error
		delivered, err = s.turnDelivered(tc)
		if err != nil {
			return ipc.TurnAck{}, err
		}
	}
	end := endDone
	if req.Status == "error" && !continuing {
		end = endFailed
	}
	var notice *turnStatus
	if req.Status == "refused" {
		slog.Warn("agent turn refused", "folder", tc.Folder, "turn_id", turnID, "err", req.Error)
		notice = &turnStatus{phase: "final", cause: "The model refused this request (provider safety filter)"}
		end = endFailed
	}
	if req.Status == "budget" {
		slog.Warn("agent turn stopped by spent budget", "folder", tc.Folder, "turn_id", turnID, "err", req.Error)
		notice = &turnStatus{phase: "final", cause: "Budget reached for today"}
		end = endFailed
	}
	if req.Status == "success" && !continuing {
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
	if continuing {
		status, failure = "checkpoint", ""
	}
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
		if !continuing {
			finishTurnLocked(s.db, s.deliver, turnID, end)
		}
		if s.loop != nil {
			s.loop.publishRoundDone(tc.ChatJID, turnID)
		}
	}
	return ipc.TurnAck{OK: true}, nil
}

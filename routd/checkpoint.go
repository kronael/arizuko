package routd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func checkpointTurnID(parent string) string {
	return fmt.Sprintf("%s%x", core.CheckpointPrefix, sha256.Sum256([]byte(parent)))
}

func (l *Loop) continuationCount(tc TurnContext) (int, error) {
	count := 0
	before := l.q.now().Add(time.Nanosecond)
	for {
		messages, err := l.db.MessagesBefore(tc.ChatJID, core.TS(before), 100)
		if err != nil {
			return 0, err
		}
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Topic != tc.Topic {
				continue
			}
			if strings.HasPrefix(message.ID, core.CheckpointPrefix) && message.BotMsg && message.Sender == store.SystemSender {
				count++
			} else if !message.BotMsg && !message.FromMe && !slices.Contains(store.SyntheticSenders, message.Sender) {
				return count, nil
			}
		}
		if len(messages) < 100 {
			return count, nil
		}
		before = messages[0].Timestamp
	}
}

func (l *Loop) continueTurn(tc TurnContext) error {
	unlockFolder := lockTurn("\x00checkpoint:" + tc.Folder)
	defer unlockFolder()
	unlock := lockTurn(tc.TurnID)
	defer unlock()
	current, exists := l.db.GetTurnContext(tc.TurnID)
	if !exists || current.StatusOwed == "stopped" ||
		hasStoppedStatus(l.db, returnTarget(current, current.ChatJID), []string{tc.TurnID}) {
		return nil
	}
	id := checkpointTurnID(tc.TurnID)
	if _, exists := l.db.GetTurnContext(id); exists {
		finishTurnLocked(l.db, l.deliver, tc.TurnID, endDone)
		return nil
	}
	count, err := l.continuationCount(tc)
	if err != nil {
		return err
	}
	if count >= 8 {
		if err := l.publishStatusLocked(tc, turnStatus{phase: "final",
			cause: "Work stopped after eight continuations. Send a new message in this topic to resume."}); err != nil {
			return err
		}
		finishTurnLocked(l.db, l.deliver, tc.TurnID, endDone)
		return nil
	}
	message, exists := l.db.MessageByID(tc.ChatJID, id)
	if !exists {
		input, err := l.db.store().DeferredMessages(context.Background(), tc.InputKeys)
		if err != nil {
			return err
		}
		message = core.Message{ID: id, ChatJID: tc.ChatJID,
			Sender: store.SystemSender, BotMsg: true, Topic: tc.Topic,
			Timestamp: l.q.now(), Verb: "message",
			Content: "[continuation] Continue the work in this same session from your last progress summary and work notes. Finish the remaining work."}
		for _, source := range input {
			message.Hops = max(message.Hops, source.Hops)
			message.LinkContext = source.LinkContext
		}
		if err := l.db.PutMessage(message); err != nil {
			return err
		}
		message, exists = l.db.MessageByID(tc.ChatJID, id)
		if !exists {
			return fmt.Errorf("checkpoint input %s disappeared", id)
		}
	}
	keys, err := turnMessageKeys([]core.Message{message})
	if err != nil {
		return err
	}
	next := TurnContext{TurnID: id, Folder: tc.Folder, Topic: tc.Topic,
		ChatJID: tc.ChatJID, Trigger: tc.Trigger, ReturnTo: tc.ReturnTo,
		TriggerMsgID: tc.TriggerMsgID,
		SessionID:    tc.SessionID,
		InputLast:    tc.InputLast, InputKeys: keys, Addressed: tc.Addressed,
		NextAttemptAt: l.q.now()}
	ok, err := l.db.store().SavePendingTurn(context.Background(), next, l.db.GetAgentCursor(tc.ChatJID))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("save continuation %s refused", id)
	}
	l.q.mu.Lock()
	key := deferredWorkPrefix + id
	if !l.q.getGroup(key).active && !slices.Contains(l.q.waitingGroups, key) {
		l.q.pending[key] = turnReservation{folder: tc.Folder, cron: core.IsScheduledSender(tc.Trigger), turnID: id}
		l.q.waitingGroups = append(l.q.waitingGroups, key)
	}
	l.q.mu.Unlock()
	finishTurnLocked(l.db, l.deliver, tc.TurnID, endDone)
	return nil
}

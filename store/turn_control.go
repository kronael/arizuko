package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

type TurnControlState struct {
	Phase          string
	Episode        string
	NextProbeAt    time.Time
	ProbeTurnID    string
	ProbeAttemptID string
	Successor      bool
}

type TurnAdmission struct {
	AttemptID     string
	InputLast     string
	InputKeys     []objstore.Key
	AmbientKeys   []objstore.Key
	Controlled    bool
	ControlBefore *TurnControlState
	ControlAfter  *TurnControlState
}

func controlAdmission(before, after *TurnControlState) ([]objstore.Check, []objstore.Write) {
	if after != nil {
		return nil, []objstore.Write{controlWrite(before, *after)}
	}
	cond := controlWrite(before, TurnControlState{}).Cond
	return []objstore.Check{{Entity: TurnControl, Key: turnControlKey(), Cond: cond}}, nil
}

func turnControlKey() objstore.Key { return objstore.Key{Partition: OwnerRoutd, Sort: "singleton"} }

func (s *Store) ReadTurnControl(ctx context.Context) (TurnControlState, bool, error) {
	it, ok, err := s.itemAt(ctx, TurnControl, turnControlKey())
	if err != nil || !ok {
		return TurnControlState{}, ok, err
	}
	at, err := time.Parse(time.RFC3339Nano, it.Attrs["next_probe_at"])
	if it.Attrs["next_probe_at"] != "" && err != nil {
		return TurnControlState{}, false, err
	}
	return TurnControlState{Phase: it.Attrs["phase"], Episode: it.Attrs["episode"], NextProbeAt: at,
		ProbeTurnID: it.Attrs["probe_turn_id"], ProbeAttemptID: it.Attrs["probe_attempt_id"],
		Successor: it.Attrs["successor"] == flagSet}, true, nil
}

func controlAttrs(state TurnControlState) map[string]string {
	attrs := map[string]string{"phase": state.Phase, "episode": state.Episode}
	if !state.NextProbeAt.IsZero() {
		attrs["next_probe_at"] = core.TS(state.NextProbeAt)
	}
	setAttr(attrs, "probe_turn_id", state.ProbeTurnID)
	setAttr(attrs, "probe_attempt_id", state.ProbeAttemptID)
	if state.Successor {
		attrs["successor"] = flagSet
	}
	return attrs
}

func controlWrite(before *TurnControlState, after TurnControlState) objstore.Write {
	attrs := controlAttrs(after)
	write := objstore.Write{Op: objstore.Put, Entity: TurnControl, Key: turnControlKey(), Attrs: attrs, Cond: objstore.Cond{Item: objstore.ItemAbsent}}
	if before != nil {
		write.Op = objstore.Update
		write.Cond = objstore.Cond{}
		old := controlAttrs(*before)
		for _, name := range []string{"phase", "episode", "next_probe_at", "probe_turn_id", "probe_attempt_id", "successor"} {
			cmp := objstore.Cmp{Attr: name, Op: objstore.Eq, Value: old[name]}
			if _, ok := old[name]; !ok {
				cmp.Op = objstore.Absent
			}
			write.Cond.Where = append(write.Cond.Where, cmp)
		}
		write.Remove = missingAttrs(attrs, "next_probe_at", "probe_turn_id", "probe_attempt_id", "successor")
	}
	return write
}

func (s *Store) WriteTurnControl(ctx context.Context, before *TurnControlState, after TurnControlState) (bool, error) {
	return s.TryWriteEntities(ctx, nil, controlWrite(before, after))
}

// InterruptTurn fences recovery against the observed turn lifecycle and control.
func (s *Store) InterruptTurn(ctx context.Context, tc TurnContext, before *TurnControlState, after TurnControlState) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	entity, it, exists, err := s.findTurn(ctx, key)
	if err != nil || !exists {
		return false, err
	}
	current, err := turnFrom(it)
	if err != nil {
		return false, err
	}
	if current.State != tc.State || current.AttemptID != tc.AttemptID || current.RunReturned != tc.RunReturned || current.Folder != tc.Folder {
		return false, nil
	}
	checks := []objstore.Check{{Entity: entity, Key: key, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: unchanged(it.Attrs)}}}
	return s.TryWriteEntities(ctx, checks, controlWrite(before, after))
}

type DeferredTurn struct {
	Turn          TurnContext
	PriorCursor   string
	ControlBefore *TurnControlState
	ControlAfter  *TurnControlState
}

func (s *Store) SavePendingTurn(ctx context.Context, tc TurnContext, priorCursor string) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	chat, err := chatKey(tc.ChatJID)
	if err != nil {
		return false, err
	}
	if tc.InputLast == "" || tc.NextAttemptAt.IsZero() || len(tc.InputKeys) == 0 {
		return false, fmt.Errorf("save pending turn %s: missing input or due time", tc.TurnID)
	}
	input, err := json.Marshal(tc.InputKeys)
	if err != nil {
		return false, err
	}
	ambient, err := json.Marshal(tc.AmbientKeys)
	if err != nil {
		return false, err
	}
	attrs := map[string]string{
		attrTurnState: TurnPendingRetry, "attempt_id": rand.Text(),
		attrTurnFolder: tc.Folder, attrTurnChatJID: tc.ChatJID,
		"input_last": tc.InputLast, "input_keys": string(input),
		"ambient_keys": string(ambient), "next_attempt_at": core.TS(tc.NextAttemptAt),
		AttrTurnStartedAt: core.TS(tc.NextAttemptAt),
	}
	setAttr(attrs, attrTurnTopic, tc.Topic)
	setAttr(attrs, attrTurnTrigger, tc.Trigger)
	setAttr(attrs, attrTurnTriggerMsg, tc.TriggerMsgID)
	setAttr(attrs, attrTurnReturnTo, tc.ReturnTo)
	if tc.Addressed {
		attrs[attrTurnAddressed] = flagSet
	}
	cursor := objstore.Cmp{Attr: AttrChatAgentCursor, Op: objstore.Eq, Value: priorCursor}
	if priorCursor == "" {
		cursor.Op = objstore.Absent
	}
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}},
		objstore.Write{Op: objstore.Put, Entity: TurnContexts, Key: key, Attrs: attrs, Cond: objstore.Cond{Item: objstore.ItemAbsent}},
		objstore.Write{Op: objstore.Update, Entity: Chats, Key: chat, Attrs: map[string]string{AttrChatAgentCursor: max(priorCursor, tc.InputLast)}, Cond: objstore.Cond{Where: []objstore.Cmp{cursor}}})
}

func (s *Store) RecordControlledProviderFailure(ctx context.Context, tc TurnContext, before, after *ModelPause, controlBefore, controlAfter *TurnControlState) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	if tc.AttemptID == "" || tc.ProviderFailureAttempt == tc.AttemptID || after == nil || after.Folder != tc.Folder || controlAfter == nil {
		return false, fmt.Errorf("record provider failure %s: invalid binding", tc.TurnID)
	}
	marker := objstore.Cmp{Attr: "provider_failure_attempt", Op: objstore.Eq, Value: tc.ProviderFailureAttempt}
	if tc.ProviderFailureAttempt == "" {
		marker.Op = objstore.Absent
	}
	pause := modelPauseWrite(after)
	pause.Cond = modelPauseCond(before)
	if before != nil {
		pause.Op = objstore.Update
		pause.Remove = missingAttrs(pause.Attrs, "next_attempt_at")
	}
	attrs := map[string]string{"provider_failure_attempt": tc.AttemptID, "status_owed": "waiting", "failure_cause": tc.FailureCause}
	var remove []string
	if tc.NextAttemptAt.IsZero() {
		remove = []string{"next_attempt_at"}
	} else {
		attrs["next_attempt_at"] = core.TS(tc.NextAttemptAt)
	}
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}},
		pause, controlWrite(controlBefore, *controlAfter), objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs: attrs, Remove: remove,
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
				{Attr: attrTurnRunReturned, Op: objstore.Absent},
				{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
				{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}, marker,
			}},
		})
}

func (s *Store) FinishControlledTurn(ctx context.Context, tc TurnContext, before, after *TurnControlState) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	if tc.AttemptID == "" || (!IsActiveTurnState(tc.State) && tc.State != TurnDone) {
		return false, fmt.Errorf("finish turn %s: invalid binding", tc.TurnID)
	}
	checks, writes := controlAdmission(before, after)
	checks = append(checks, objstore.Check{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}})
	attrs := map[string]string{attrTurnState: TurnDone, attrTurnRunReturned: flagSet}
	if tc.FailureCause != "" {
		attrs["failure_cause"] = tc.FailureCause
		attrs["status_owed"] = "final"
	}
	if tc.StatusOwed == "stopped" {
		attrs["status_owed"] = "stopped"
	}
	write := objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs:  attrs,
		Remove: []string{"next_attempt_at"}, Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
		}}}
	if tc.State == TurnDone {
		write.Cond.Where = append(write.Cond.Where, objstore.Cmp{Attr: attrTurnRunReturned, Op: objstore.Absent})
	}
	writes = append(writes, write)
	return s.TryWriteEntities(ctx, checks, writes...)
}

func (s *Store) DeferredTurns(ctx context.Context, now time.Time) ([]TurnContext, error) {
	return s.indexedTurns(ctx, "turns/retry", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"})
}

func (s *Store) DeferredMessages(ctx context.Context, keys []objstore.Key) ([]core.Message, error) {
	messages := make([]core.Message, 0, len(keys))
	for _, key := range keys {
		item, ok, err := s.itemAt(ctx, Messages, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("deferred message %q/%q is missing", key.Partition, key.Sort)
		}
		messages = append(messages, messageFrom(item))
	}
	return messages, nil
}

func (s *Store) DeferTurn(ctx context.Context, d DeferredTurn) (bool, error) {
	tc := d.Turn
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	chat, err := chatKey(tc.ChatJID)
	if err != nil {
		return false, err
	}
	if tc.AttemptID == "" || tc.InputLast == "" || tc.NextAttemptAt.IsZero() {
		return false, fmt.Errorf("defer turn %s: missing attempt, cursor or due time", tc.TurnID)
	}
	input, err := json.Marshal(tc.InputKeys)
	if err != nil {
		return false, err
	}
	ambient, err := json.Marshal(tc.AmbientKeys)
	if err != nil {
		return false, err
	}
	cursor := objstore.Cmp{Attr: AttrChatAgentCursor, Op: objstore.Eq, Value: d.PriorCursor}
	if d.PriorCursor == "" {
		cursor.Op = objstore.Absent
	}
	count := objstore.Cmp{Attr: attrTurnRetryCount, Op: objstore.Eq, Value: strconv.Itoa(tc.RetryCount)}
	if tc.RetryCount == 0 {
		count.Op = objstore.Absent
	}
	writes := []objstore.Write{
		{Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: map[string]string{
			attrTurnState: TurnPendingRetry, attrTurnRetryCount: strconv.Itoa(tc.RetryCount + 1),
			"input_keys": string(input), "ambient_keys": string(ambient), "input_last": tc.InputLast,
			"next_attempt_at": core.TS(tc.NextAttemptAt), "failure_cause": tc.FailureCause, "status_owed": "waiting",
		}, Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			{Attr: attrTurnChatJID, Op: objstore.Eq, Value: tc.ChatJID},
			count,
		}}},
		{Op: objstore.Update, Entity: Chats, Key: chat, Attrs: map[string]string{AttrChatAgentCursor: max(d.PriorCursor, tc.InputLast)}, Cond: objstore.Cond{Where: []objstore.Cmp{cursor}}},
	}
	if d.ControlAfter != nil {
		writes = append(writes, controlWrite(d.ControlBefore, *d.ControlAfter))
	}
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}}, writes...)
}

func (s *Store) ClaimDeferredTurn(ctx context.Context, tc TurnContext, attemptID string, now time.Time) (bool, error) {
	return s.claimDeferredTurn(ctx, tc, nil, &TurnAdmission{AttemptID: attemptID}, now, nil, nil)
}

func (s *Store) RecordCapacityFailure(ctx context.Context, tc TurnContext, priorCursor, wakeCursor string, decision retry.Decision) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	_, validInput := CursorInstant(tc.InputLast)
	_, validWake := CursorInstant(wakeCursor)
	if tc.AttemptID == "" || tc.ChatJID == "" || requireMessageCursor(tc.InputLast) != nil ||
		!validInput || (wakeCursor != "" && (requireMessageCursor(wakeCursor) != nil || !validWake)) ||
		decision.Failures != tc.CapacityFailures+1 || (!decision.Stop && decision.NextAt.IsZero()) {
		return false, fmt.Errorf("record capacity failure %s: invalid binding", tc.TurnID)
	}
	input, err := json.Marshal(tc.InputKeys)
	if err != nil || len(tc.InputKeys) == 0 {
		return false, fmt.Errorf("record capacity failure %s: invalid input", tc.TurnID)
	}
	for _, inputKey := range tc.InputKeys {
		_, valid := CursorInstant(inputKey.Sort)
		if inputKey.Partition != tc.ChatJID || requireMessageCursor(inputKey.Sort) != nil || !valid {
			return false, fmt.Errorf("record capacity failure %s: invalid input", tc.TurnID)
		}
	}
	if tc.InputKeys[len(tc.InputKeys)-1].Sort != tc.InputLast {
		return false, fmt.Errorf("record capacity failure %s: invalid input last", tc.TurnID)
	}
	attrs := map[string]string{attrTurnState: TurnWaitingCapacity, "capacity_failures": strconv.Itoa(decision.Failures)}
	setAttr(attrs, "capacity_wake_cursor", wakeCursor)
	var remove []string
	var writes []objstore.Write
	if decision.Stop {
		attrs[attrTurnState] = TurnDone
		attrs[attrTurnRunReturned] = flagSet
		attrs["failure_cause"] = "Execution capacity unavailable"
		attrs["status_owed"] = "final"
		remove = append(remove, "next_attempt_at")
		chat, err := chatKey(tc.ChatJID)
		if err != nil {
			return false, err
		}
		writes = append(writes, objstore.Write{Op: objstore.Update, Entity: Chats, Key: chat,
			Attrs: map[string]string{AttrChatAgentCursor: max(priorCursor, tc.InputLast)},
			Cond:  objstore.Cond{Where: []objstore.Cmp{attrIs(AttrChatAgentCursor, priorCursor)}}})
	} else {
		attrs["next_attempt_at"] = core.TS(decision.NextAt)
	}
	count := ""
	if tc.CapacityFailures > 0 {
		count = strconv.Itoa(tc.CapacityFailures)
	}
	writes = append(writes, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: attrs, Remove: remove, Cond: objstore.Cond{Where: []objstore.Cmp{
			attrIs(objstore.IndexRevision, tc.Revision), attrIs(attrTurnState, TurnRunning),
			attrIs("attempt_id", tc.AttemptID), attrIs(attrTurnChatJID, tc.ChatJID),
			attrIs("input_last", tc.InputLast), attrIs("input_keys", string(input)),
			attrIs("capacity_failures", count), {Attr: attrTurnRunID, Op: objstore.Absent},
			{Attr: attrTurnRunReturned, Op: objstore.Absent},
		}}})
	return s.TryWriteEntities(ctx, nil, writes...)
}

func (s *Store) WakeCapacityTurn(ctx context.Context, observed TurnContext, cursor string, now time.Time) (bool, error) {
	_, valid := CursorInstant(cursor)
	if requireMessageCursor(cursor) != nil || !valid {
		return false, fmt.Errorf("invalid capacity wake cursor")
	}
	if observed.State != TurnWaitingCapacity || cursor <= observed.CapacityWakeCursor {
		return false, nil
	}
	if observed.NextAttemptAt.Before(now) {
		now = observed.NextAttemptAt
	}
	key, err := turnKey(observed.TurnID)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{"next_attempt_at": core.TS(now), "capacity_wake_cursor": cursor},
		Cond: objstore.Cond{Where: []objstore.Cmp{attrIs(objstore.IndexRevision, observed.Revision),
			attrIs(attrTurnState, TurnWaitingCapacity), attrIs("attempt_id", observed.AttemptID)}}})
}

func (s *Store) SupersedeCapacityTurn(ctx context.Context, observed TurnContext) (bool, error) {
	if observed.State != TurnWaitingCapacity {
		return false, nil
	}
	key, err := turnKey(observed.TurnID)
	if err != nil {
		return false, err
	}
	return s.writeTurn(ctx, nil, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnDone, attrTurnRunReturned: flagSet}, Remove: []string{"next_attempt_at"},
		Cond: objstore.Cond{Where: []objstore.Cmp{attrIs(objstore.IndexRevision, observed.Revision),
			attrIs(attrTurnState, TurnWaitingCapacity), attrIs("attempt_id", observed.AttemptID),
			{Attr: attrTurnRunID, Op: objstore.Absent}, {Attr: attrTurnRunReturned, Op: objstore.Absent}}}})
}

func (s *Store) RequeueDeferredTurn(ctx context.Context, tc TurnContext, due time.Time, priorCursor string) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	where := []objstore.Cmp{
		{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
		{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
		{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
	}
	var writes []objstore.Write
	if tc.State == TurnPendingRetry || tc.State == TurnWaitingCapacity {
		where = append(where, attrIs("next_attempt_at", core.TS(tc.NextAttemptAt)))
	}
	if tc.State == TurnWaitingCapacity {
		chat, err := chatKey(tc.ChatJID)
		if err != nil {
			return false, err
		}
		where = append(where, attrIs(objstore.IndexRevision, tc.Revision),
			objstore.Cmp{Attr: attrTurnRunID, Op: objstore.Absent},
			objstore.Cmp{Attr: attrTurnRunReturned, Op: objstore.Absent})
		writes = append(writes, objstore.Write{Op: objstore.Update, Entity: Chats, Key: chat,
			Attrs: map[string]string{AttrChatAgentCursor: max(priorCursor, tc.InputLast)},
			Cond:  objstore.Cond{Where: []objstore.Cmp{attrIs(AttrChatAgentCursor, priorCursor)}}})
	}
	writes = append(writes, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnPendingRetry, "next_attempt_at": core.TS(due)},
		Cond:  objstore.Cond{Where: where},
	})
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group,
		Cond: objstore.Cond{Item: objstore.ItemPresent}}}, writes...)
}

func (s *Store) ClaimControlledTurn(ctx context.Context, tc TurnContext, binding *TurnContext,
	admission *TurnAdmission, now time.Time) (bool, error) {
	if admission == nil {
		return false, fmt.Errorf("claim turn %s: missing admission", tc.TurnID)
	}
	checks, writes := controlAdmission(admission.ControlBefore, admission.ControlAfter)
	return s.claimDeferredTurn(ctx, tc, binding, admission, now, checks, writes)
}

func (s *Store) claimDeferredTurn(ctx context.Context, tc TurnContext, binding *TurnContext,
	admission *TurnAdmission, now time.Time, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	if admission == nil || admission.AttemptID == "" || tc.AttemptID == "" || (tc.State != TurnPendingRetry && tc.State != TurnWaitingCapacity) {
		return false, fmt.Errorf("claim turn %s: invalid binding", tc.TurnID)
	}
	var update objstore.Write
	if binding != nil {
		if tc.State != TurnWaitingCapacity || binding.TurnID != tc.TurnID {
			return false, fmt.Errorf("claim turn %s: invalid replacement", tc.TurnID)
		}
		var bindingChecks []objstore.Check
		var bindingWrites []objstore.Write
		update, bindingChecks, bindingWrites, err = s.prepareTurnBinding(ctx, *binding, admission)
		if err != nil {
			return false, err
		}
		checks = append(checks, bindingChecks...)
		writes = append(writes, bindingWrites...)
	} else {
		group, err := groupKey(tc.Folder)
		if err != nil {
			return false, err
		}
		checks = append(checks, objstore.Check{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}})
		update = objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs:  map[string]string{attrTurnState: TurnRunning, "attempt_id": admission.AttemptID},
			Remove: []string{attrTurnRunReturned, attrTurnRunID}}
	}
	update.Attrs[AttrTurnStartedAt] = core.TS(now)
	update.Remove = append(update.Remove, "next_attempt_at")
	if tc.RetryCount > 0 {
		update.Attrs["status_owed"] = "running"
	}
	count := ""
	if tc.CapacityFailures > 0 {
		count = strconv.Itoa(tc.CapacityFailures)
	}
	update.Cond = objstore.Cond{Where: []objstore.Cmp{
		{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
		{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
		{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
		attrIs(objstore.IndexRevision, tc.Revision),
		attrIs("capacity_failures", count),
		attrIs("next_attempt_at", core.TS(tc.NextAttemptAt)),
		{Attr: "next_attempt_at", Op: objstore.Le, Value: core.TS(now)},
	}}
	return s.TryWriteEntities(ctx, checks, append(writes, update)...)
}

// OwedTurnStatuses includes completed turns whose lifecycle status still needs publication.
func (s *Store) OwedTurnStatuses(ctx context.Context) ([]TurnContext, error) {
	return s.indexedTurns(ctx, "turns/status", objstore.KeyPredicate{})
}

func (s *Store) AcknowledgeTurnStatus(ctx context.Context, tc TurnContext) error {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return err
	}
	count := objstore.Cmp{Attr: attrTurnRetryCount, Op: objstore.Eq, Value: strconv.Itoa(tc.RetryCount)}
	if tc.RetryCount == 0 {
		count.Op = objstore.Absent
	}
	attempt := objstore.Cmp{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}
	if tc.AttemptID == "" {
		attempt.Op = objstore.Absent
	}

	_, err = s.writeTurn(ctx, nil, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key, Remove: []string{"status_owed"}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
		attempt, {Attr: "status_owed", Op: objstore.Eq, Value: tc.StatusOwed}, {Attr: attrTurnState, Op: objstore.Eq, Value: tc.State}, count,
	}}})
	return err
}

func (s *Store) RecordControlledStatus(ctx context.Context, tc TurnContext, before *TurnControlState, after TurnControlState) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	return s.writeTurn(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}},
		[]objstore.Write{controlWrite(before, after)},
		objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: map[string]string{"status_owed": "final", "failure_cause": tc.FailureCause}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}, {Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
		}}},
	)
}

func (s *Store) FinishTurnStatus(ctx context.Context, tc TurnContext) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	phase := "cleared"
	if tc.StatusOwed == "final" || tc.StatusOwed == "stopped" {
		phase = tc.StatusOwed
	}
	owed := objstore.Cmp{Attr: "status_owed", Op: objstore.Eq, Value: tc.StatusOwed}
	if tc.StatusOwed == "" {
		owed.Op = objstore.Absent
	}
	attempt := objstore.Cmp{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}
	if tc.AttemptID == "" {
		attempt.Op = objstore.Absent
	}
	return s.writeTurn(ctx, nil, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnDone, "status_owed": phase}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State}, attempt, owed,
		}}})
}

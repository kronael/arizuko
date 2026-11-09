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
)

type TurnControlState struct {
	Phase          string
	Episode        string
	NextProbeAt    time.Time
	ProbeTurnID    string
	ProbeAttemptID string
}

type TurnAdmission struct {
	AttemptID     string
	InputLast     string
	ProbeEpisode  string
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
		ProbeTurnID: it.Attrs["probe_turn_id"], ProbeAttemptID: it.Attrs["probe_attempt_id"]}, true, nil
}

func controlAttrs(state TurnControlState) map[string]string {
	attrs := map[string]string{"phase": state.Phase, "episode": state.Episode}
	if !state.NextProbeAt.IsZero() {
		attrs["next_probe_at"] = core.TS(state.NextProbeAt)
	}
	setAttr(attrs, "probe_turn_id", state.ProbeTurnID)
	setAttr(attrs, "probe_attempt_id", state.ProbeAttemptID)
	return attrs
}

func controlWrite(before *TurnControlState, after TurnControlState) objstore.Write {
	attrs := controlAttrs(after)
	write := objstore.Write{Op: objstore.Put, Entity: TurnControl, Key: turnControlKey(), Attrs: attrs, Cond: objstore.Cond{Item: objstore.ItemAbsent}}
	if before != nil {
		write.Op = objstore.Update
		write.Cond = objstore.Cond{}
		old := controlAttrs(*before)
		for _, name := range []string{"phase", "episode", "next_probe_at", "probe_turn_id", "probe_attempt_id"} {
			cmp := objstore.Cmp{Attr: name, Op: objstore.Eq, Value: old[name]}
			if _, ok := old[name]; !ok {
				cmp.Op = objstore.Absent
			}
			write.Cond.Where = append(write.Cond.Where, cmp)
		}
		write.Remove = missingAttrs(attrs, "next_probe_at", "probe_turn_id", "probe_attempt_id")
	}
	return write
}

func (s *Store) WriteTurnControl(ctx context.Context, before *TurnControlState, after TurnControlState) (bool, error) {
	return s.TryWriteEntities(ctx, nil, controlWrite(before, after))
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
		pause.Remove = missingAttrs(pause.Attrs, "next_attempt_at", "probe_turn_id", "probe_attempt_id")
	}
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}},
		pause, controlWrite(controlBefore, *controlAfter), objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs: map[string]string{"provider_failure_attempt": tc.AttemptID},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
				{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
				{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}, marker,
			}},
		})
}

func (s *Store) DeferredTurns(ctx context.Context, now time.Time) ([]TurnContext, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTurnState, Op: objstore.Eq, Value: TurnPendingRetry},
			{Attr: "next_attempt_at", Op: objstore.Le, Value: core.TS(now)}}})
	if err != nil {
		return nil, err
	}
	turns := make([]TurnContext, 0, len(items))
	for _, item := range items {
		turns = append(turns, turnFrom(item))
	}
	return turns, nil
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
			"next_attempt_at": core.TS(tc.NextAttemptAt), "failure_cause": tc.FailureCause,
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
	return s.claimDeferredTurn(ctx, tc, attemptID, now, nil, nil)
}

func (s *Store) ClaimControlledTurn(ctx context.Context, tc TurnContext, attemptID string, now time.Time, before, after *TurnControlState) (bool, error) {
	checks, writes := controlAdmission(before, after)
	return s.claimDeferredTurn(ctx, tc, attemptID, now, checks, writes)
}

func (s *Store) claimDeferredTurn(ctx context.Context, tc TurnContext, attemptID string, now time.Time, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	if attemptID == "" || tc.AttemptID == "" {
		return false, fmt.Errorf("claim turn %s: missing attempt", tc.TurnID)
	}
	checks = append(checks, objstore.Check{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}})
	writes = append(writes, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs:  map[string]string{attrTurnState: TurnRunning, "attempt_id": attemptID, AttrTurnStartedAt: core.TS(now)},
		Remove: []string{attrTurnRunReturned, attrTurnRunID, "next_attempt_at"},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnPendingRetry},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
			{Attr: "next_attempt_at", Op: objstore.Le, Value: core.TS(now)},
		}},
	})
	return s.TryWriteEntities(ctx, checks, writes...)
}

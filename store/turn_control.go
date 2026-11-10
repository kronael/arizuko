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
	Successor      bool
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

type DeferredTurn struct {
	Turn          TurnContext
	PriorCursor   string
	ControlBefore *TurnControlState
	ControlAfter  *TurnControlState
	ReserveRetry  bool
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

func (s *Store) MigrateDeferredTurn(ctx context.Context, d DeferredTurn) (bool, error) {
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
	if (tc.State != TurnPendingRetry && tc.State != TurnHeld) || tc.InputLast == "" || len(tc.InputKeys) == 0 || tc.NextAttemptAt.IsZero() {
		return false, fmt.Errorf("migrate turn %s: invalid binding", tc.TurnID)
	}
	input, err := json.Marshal(tc.InputKeys)
	if err != nil {
		return false, err
	}
	ambient, err := json.Marshal(tc.AmbientKeys)
	if err != nil {
		return false, err
	}
	attempt := objstore.Cmp{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}
	if tc.AttemptID == "" {
		attempt.Op = objstore.Absent
		tc.AttemptID = rand.Text()
	}
	count := objstore.Cmp{Attr: attrTurnRetryCount, Op: objstore.Eq, Value: strconv.Itoa(tc.RetryCount)}
	if tc.RetryCount == 0 {
		count.Op = objstore.Absent
	}
	cursor := objstore.Cmp{Attr: AttrChatAgentCursor, Op: objstore.Eq, Value: d.PriorCursor}
	if d.PriorCursor == "" {
		cursor.Op = objstore.Absent
	}
	prior, _, err := s.itemAt(ctx, TurnContexts, key)
	if err != nil {
		return false, err
	}
	inputGuard := objstore.Cmp{Attr: "input_keys", Op: objstore.Absent}
	if value, ok := prior.Attrs["input_keys"]; ok {
		inputGuard.Op, inputGuard.Value = objstore.Eq, value
	}
	checks, writes := controlAdmission(d.ControlBefore, d.ControlAfter)
	checks = append(checks, objstore.Check{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}})
	attrs := map[string]string{attrTurnState: TurnPendingRetry, "attempt_id": tc.AttemptID, "input_last": tc.InputLast,
		"input_keys": string(input), "ambient_keys": string(ambient), "next_attempt_at": core.TS(tc.NextAttemptAt)}
	if d.ReserveRetry {
		attrs[attrTurnRetryCount] = strconv.Itoa(tc.RetryCount + 1)
	}
	writes = append(writes, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: attrs,
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			{Attr: attrTurnChatJID, Op: objstore.Eq, Value: tc.ChatJID},
			{Attr: "next_attempt_at", Op: objstore.Absent}, inputGuard, attempt, count,
		}}}, objstore.Write{Op: objstore.Update, Entity: Chats, Key: chat,
		Attrs: map[string]string{AttrChatAgentCursor: max(d.PriorCursor, tc.InputLast)},
		Cond:  objstore.Cond{Where: []objstore.Cmp{cursor}}})
	return s.TryWriteEntities(ctx, checks, writes...)
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
	if tc.AttemptID == "" || (tc.State != TurnRunning && tc.State != TurnPendingRetry && tc.State != TurnDone) {
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
	return s.claimDeferredTurn(ctx, tc, attemptID, now, nil, nil)
}

func (s *Store) RequeueDeferredTurn(ctx context.Context, tc TurnContext, due time.Time) (bool, error) {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return false, err
	}
	group, err := groupKey(tc.Folder)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group,
		Cond: objstore.Cond{Item: objstore.ItemPresent}}}, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnPendingRetry, "next_attempt_at": core.TS(due)},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
		}},
	})
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
	attrs := map[string]string{attrTurnState: TurnRunning, "attempt_id": attemptID, AttrTurnStartedAt: core.TS(now)}
	if tc.RetryCount > 0 {
		attrs["status_owed"] = "running"
	}
	writes = append(writes, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs:  attrs,
		Remove: []string{attrTurnRunReturned, attrTurnRunID, "next_attempt_at", "probe_episode"},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnPendingRetry},
			{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
			{Attr: "next_attempt_at", Op: objstore.Le, Value: core.TS(now)},
		}},
	})
	return s.TryWriteEntities(ctx, checks, writes...)
}

// OwedTurnStatuses includes completed turns whose lifecycle status still needs publication.
func (s *Store) OwedTurnStatuses(ctx context.Context) ([]TurnContext, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnContexts, Partition: OwnerRoutd, Where: []objstore.Cmp{{Attr: "status_owed", Op: objstore.Present}}})
	if err != nil {
		return nil, err
	}
	var turns []TurnContext
	for _, it := range items {
		turns = append(turns, turnFrom(it))
	}
	return turns, nil
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
	// Stale acknowledgements must leave current intent unchanged.
	_, err = s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key, Remove: []string{"status_owed"}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
		{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID}, {Attr: "status_owed", Op: objstore.Eq, Value: tc.StatusOwed}, {Attr: attrTurnState, Op: objstore.Eq, Value: tc.State}, count,
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
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}},
		controlWrite(before, after),
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
	return s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnDone, "status_owed": phase}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State}, attempt, owed,
		}}})
}

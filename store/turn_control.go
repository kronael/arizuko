package store

import (
	"context"
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
	return s.TryWriteEntities(ctx, []objstore.Check{{Entity: Groups, Key: group, Cond: objstore.Cond{Item: objstore.ItemPresent}}}, objstore.Write{
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
}

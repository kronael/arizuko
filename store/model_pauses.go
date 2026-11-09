package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

const retryRounds = 8

var ErrModelPaused = errors.New("model access is paused")

func modelPauseKey(folder string) objstore.Key {
	return objstore.Key{Partition: OwnerRoutd, Sort: folder}
}

type ModelPause struct {
	Folder         string
	Reason         string
	OpenedAt       string
	NextAttemptAt  string
	Attempts       int
	Evidence       string
	ProbeTurnID    string
	ProbeAttemptID string
}

func (s *Store) ReadModelPause(ctx context.Context, folder string) (*ModelPause, error) {
	it, ok, err := s.itemAt(ctx, ModelPauses, modelPauseKey(folder))
	if err != nil || !ok {
		return nil, err
	}
	return decodeModelPause(it)
}

func (s *Store) ListModelPauses(ctx context.Context) ([]ModelPause, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: ModelPauses, Partition: OwnerRoutd})
	if err != nil {
		return nil, err
	}
	var pauses []ModelPause
	for _, it := range items {
		p, err := decodeModelPause(it)
		if err != nil {
			return nil, err
		}
		pauses = append(pauses, *p)
	}
	return pauses, nil
}

func decodeModelPause(it objstore.Item) (*ModelPause, error) {
	n, err := strconv.Atoi(it.Attrs["attempts"])
	if err != nil {
		return nil, fmt.Errorf("read model pause attempts: %w", err)
	}
	return &ModelPause{
		Folder: it.Key.Sort, Reason: it.Attrs["reason"], OpenedAt: it.Attrs["opened_at"],
		NextAttemptAt: it.Attrs["next_attempt_at"], Attempts: n,
		Evidence: it.Attrs["evidence"], ProbeTurnID: it.Attrs["probe_turn_id"],
		ProbeAttemptID: it.Attrs["probe_attempt_id"],
	}, nil
}

func modelPauseCond(p *ModelPause) objstore.Cond {
	if p == nil {
		return objstore.Cond{Item: objstore.ItemAbsent}
	}
	bound := objstore.Cmp{Attr: "probe_attempt_id", Op: objstore.Eq, Value: p.ProbeAttemptID}
	if p.ProbeAttemptID == "" {
		bound.Op = objstore.Absent
	}
	return objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
		{Attr: "opened_at", Op: objstore.Eq, Value: p.OpenedAt},
		{Attr: "attempts", Op: objstore.Eq, Value: strconv.Itoa(p.Attempts)}, bound,
	}}
}

func modelPauseWrite(p *ModelPause) objstore.Write {
	attrs := map[string]string{
		"reason": p.Reason, "opened_at": p.OpenedAt,
		"attempts": strconv.Itoa(p.Attempts), "evidence": p.Evidence,
	}
	setAttr(attrs, "next_attempt_at", p.NextAttemptAt)
	setAttr(attrs, "probe_turn_id", p.ProbeTurnID)
	setAttr(attrs, "probe_attempt_id", p.ProbeAttemptID)
	return objstore.Write{Op: objstore.Put, Entity: ModelPauses, Key: modelPauseKey(p.Folder), Attrs: attrs}
}

func modelPauseDelete(p ModelPause) objstore.Write {
	return objstore.Write{Op: objstore.Delete, Entity: ModelPauses,
		Key: modelPauseKey(p.Folder), Cond: modelPauseCond(&p)}
}

func (s *Store) ClearModelPauseInto(ws *WriteSet, p ModelPause, refused error) error {
	return ws.AddGuarded(refused, modelPauseDelete(p))
}

func (s *Store) RetireModelProbe(ctx context.Context, p ModelPause, tc TurnContext) error {
	key, err := turnKey(tc.TurnID)
	if err != nil {
		return err
	}
	state := tc.State
	if state == TurnRunning {
		state = TurnHeld
	}
	ok, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update,
		Entity: ModelPauses, Key: modelPauseKey(p.Folder),
		Remove: []string{"probe_turn_id", "probe_attempt_id"}, Cond: modelPauseCond(&p)},
		objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs: map[string]string{attrTurnState: state, attrTurnRunReturned: flagSet},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
				{Attr: "attempt_id", Op: objstore.Eq, Value: tc.AttemptID},
			}}})
	if err == nil && !ok {
		return fmt.Errorf("legacy model probe changed")
	}
	return err
}

func (s *Store) ClearControlledModelPauses(ctx context.Context, control TurnControlState) error {
	pauses, err := s.ListModelPauses(ctx)
	if err != nil {
		return err
	}
	checks, _ := controlAdmission(&control, nil)
	for _, p := range pauses {
		// A newer refusal or breaker episode keeps its metadata.
		if _, err := s.TryWriteEntities(ctx, checks, modelPauseDelete(p)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) HeldTurns(ctx context.Context, chatJID string) ([]TurnContext, error) {
	where := []objstore.Cmp{{Attr: attrTurnState, Op: objstore.Eq, Value: TurnHeld}}
	if chatJID != "" {
		where = append(where, objstore.Cmp{Attr: attrTurnChatJID, Op: objstore.Eq, Value: chatJID})
	}
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnContexts,
		Partition: OwnerRoutd, Where: where})
	var turns []TurnContext
	for _, it := range items {
		turns = append(turns, turnFrom(it))
	}
	return turns, err
}

func (s *Store) retireFolderTurns(ctx context.Context, folder string) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: TurnContexts, Partition: OwnerRoutd})
	if err != nil {
		return 0, err
	}
	retiredCount := 0
	for _, item := range items {
		for round := range retryRounds {
			tc := turnFrom(item)
			if tc.State == TurnDone || !groupfolder.ContainsSep(folder, tc.Folder, "/") {
				break
			}
			where := []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: tc.State},
				{Attr: attrTurnFolder, Op: objstore.Eq, Value: tc.Folder},
			}
			for attr, value := range map[string]string{"attempt_id": tc.AttemptID, "input_last": tc.InputLast} {
				cmp := objstore.Cmp{Attr: attr, Op: objstore.Eq, Value: value}
				if value == "" {
					cmp.Op = objstore.Absent
				}
				where = append(where, cmp)
			}
			writes := []objstore.Write{{
				Op: objstore.Update, Entity: TurnContexts, Key: item.Key,
				Attrs: map[string]string{attrTurnState: TurnDone, attrTurnRunReturned: flagSet},
				Cond:  objstore.Cond{Where: where},
			}}
			if tc.InputLast != "" {
				key, err := chatKey(tc.ChatJID)
				if err != nil {
					return 0, err
				}
				chat, exists, err := s.itemAt(ctx, Chats, key)
				if err != nil {
					return 0, err
				}
				if !exists {
					writes = append(writes, objstore.Write{Op: objstore.Put, Entity: Chats, Key: key,
						Attrs: map[string]string{AttrChatAgentCursor: tc.InputLast},
						Cond:  objstore.Cond{Item: objstore.ItemAbsent}})
				} else if cursor, present := chat.Attrs[AttrChatAgentCursor]; cursor < tc.InputLast {
					cmp := objstore.Cmp{Attr: AttrChatAgentCursor, Op: objstore.Lt, Value: tc.InputLast}
					if !present {
						cmp.Op = objstore.Absent
					}
					writes = append(writes, objstore.Write{Op: objstore.Update, Entity: Chats, Key: key,
						Attrs: map[string]string{AttrChatAgentCursor: tc.InputLast},
						Cond:  objstore.Cond{Where: []objstore.Cmp{cmp}}})
				}
			}
			retired, err := s.TryWriteEntities(ctx, nil, writes...)
			if err != nil {
				return 0, err
			}
			if retired {
				retiredCount++
				break
			}
			if round == retryRounds-1 {
				return 0, fmt.Errorf("retire turn %s: binding changed during %d writes", tc.TurnID, retryRounds)
			}
			var exists bool
			item, exists, err = s.itemAt(ctx, TurnContexts, item.Key)
			if err != nil {
				return 0, err
			}
			if !exists {
				break
			}
		}
	}
	return retiredCount, nil
}

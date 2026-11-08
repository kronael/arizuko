package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

var ErrModelPaused = errors.New("model access is paused")

var modelPauseKey = objstore.Key{Partition: OwnerRoutd, Sort: "instance"}

type ModelPause struct {
	Reason         string
	OpenedAt       string
	NextAttemptAt  string
	Attempts       int
	Evidence       string
	ProbeTurnID    string
	ProbeAttemptID string
}

type TurnAdmission struct {
	AttemptID    string
	InputLast    string
	ProbeEpisode string
}

func (s *Store) ReadModelPause(ctx context.Context) (*ModelPause, error) {
	it, ok, err := s.itemAt(ctx, ModelPauses, modelPauseKey)
	if err != nil || !ok {
		return nil, err
	}
	n, err := strconv.Atoi(it.Attrs["attempts"])
	if err != nil {
		return nil, fmt.Errorf("read model pause attempts: %w", err)
	}
	return &ModelPause{
		Reason: it.Attrs["reason"], OpenedAt: it.Attrs["opened_at"],
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
	return objstore.Write{Op: objstore.Put, Entity: ModelPauses, Key: modelPauseKey, Attrs: attrs}
}

func modelRetryAt(now time.Time, attempts int, reset time.Time) string {
	if reset.After(now) {
		return core.TS(reset)
	}
	delay := min(30*time.Second*time.Duration(1<<min(attempts-1, 7)), time.Hour)
	delay = min(delay+time.Duration(rand.Int64N(int64(delay/10)+1)), time.Hour)
	return core.TS(now.Add(delay))
}

func (s *Store) RecordProviderFailure(ctx context.Context, turnID, attempt, reason string,
	reset, now time.Time) (*ModelPause, error) {
	switch reason {
	case "spend_limit", "rate_limit", "authentication", "outage":
	default:
		return nil, fmt.Errorf("unknown provider failure %q", reason)
	}
	key, err := turnKey(turnID)
	if err != nil {
		return nil, err
	}
	for range retryRounds {
		it, ok, err := s.itemAt(ctx, TurnContexts, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no provider attempt %s", turnID)
		}
		p, err := s.ReadModelPause(ctx)
		if err != nil {
			return nil, err
		}
		if it.Attrs["attempt_id"] != attempt || it.Attrs["provider_failure_attempt"] == attempt ||
			it.Attrs[attrTurnState] != TurnRunning {
			return p, nil
		}
		before := modelPauseCond(p)
		if p == nil {
			p = &ModelPause{Reason: reason, OpenedAt: core.TS(now), Attempts: 1, Evidence: reason}
			p.NextAttemptAt = modelRetryAt(now, p.Attempts, reset)
		} else if p.ProbeTurnID == turnID && p.ProbeAttemptID == attempt {
			p.Attempts++
			p.Reason, p.Evidence = reason, reason
			p.NextAttemptAt = modelRetryAt(now, p.Attempts, reset)
			p.ProbeTurnID, p.ProbeAttemptID = "", ""
		}
		write := modelPauseWrite(p)
		if before.Item == objstore.ItemAbsent {
			write.Cond = before
		} else {
			write.Op, write.Cond = objstore.Update, before
			write.Remove = missingAttrs(write.Attrs, "next_attempt_at", "probe_turn_id", "probe_attempt_id")
		}
		moved, err := s.TryWriteEntities(ctx, nil, write, objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs: map[string]string{attrTurnState: TurnHeld, "provider_failure_attempt": attempt},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: "attempt_id", Op: objstore.Eq, Value: attempt},
				{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
			}},
		})
		if err != nil || moved {
			return p, err
		}
	}
	return nil, fmt.Errorf("provider refusal changed during %d writes", retryRounds)
}

func modelPauseDelete(p ModelPause) objstore.Write {
	return objstore.Write{Op: objstore.Delete, Entity: ModelPauses,
		Key: modelPauseKey, Cond: modelPauseCond(&p)}
}

func (s *Store) ClearModelPauseInto(ws *WriteSet, p ModelPause, refused error) error {
	return ws.AddGuarded(refused, modelPauseDelete(p))
}

func (s *Store) CompleteModelProbe(ctx context.Context, tc TurnContext) error {
	if tc.ProbeEpisode == "" {
		return nil
	}
	p, err := s.ReadModelPause(ctx)
	if err != nil || p == nil || p.OpenedAt != tc.ProbeEpisode ||
		p.ProbeTurnID != tc.TurnID || p.ProbeAttemptID != tc.AttemptID {
		return err
	}
	// A newer episode or attempt owns the row when this deletion is refused.
	_, err = s.TryWriteEntities(ctx, nil, modelPauseDelete(*p))
	return err
}

func (s *Store) RearmModelProbe(ctx context.Context, p ModelPause, now time.Time) error {
	key, err := turnKey(p.ProbeTurnID)
	if err != nil {
		return err
	}
	// A refused rearm belongs to a newer episode or a completed attempt.
	_, err = s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: ModelPauses, Key: modelPauseKey,
		Attrs:  map[string]string{"next_attempt_at": core.TS(now)},
		Remove: []string{"probe_turn_id", "probe_attempt_id"}, Cond: modelPauseCond(&p),
	}, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: TurnHeld, attrTurnRunReturned: flagSet},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: "attempt_id", Op: objstore.Eq, Value: p.ProbeAttemptID},
			{Attr: attrTurnState, Op: objstore.Ne, Value: TurnDone},
		}},
	})
	return err
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

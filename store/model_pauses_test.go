package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func openPause(t *testing.T, s *Store, id string, now time.Time) *ModelPause {
	t.Helper()
	if _, exists := s.GroupByFolder("g"); !exists {
		if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	if live, err := s.OpenTurnContext(id, "g", "", "web:g", "u", "", nil); err != nil || !live {
		t.Fatalf("open turn: %v %v", live, err)
	}
	tc, _ := s.TurnContextOf(id)
	p, err := recordPause(s, tc, now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func recordPause(s *Store, tc TurnContext, now time.Time) (*ModelPause, error) {
	p := &ModelPause{Folder: tc.Folder, Reason: "rate_limit", Evidence: "rate_limit", OpenedAt: core.TS(now), Attempts: 1}
	before, exists, err := s.ReadTurnControl(context.Background())
	if err != nil {
		return nil, err
	}
	var prior *TurnControlState
	if exists {
		prior = &before
	}
	_, err = s.RecordControlledProviderFailure(context.Background(), tc, nil, p, prior,
		&TurnControlState{Phase: "open", Episode: tc.AttemptID, NextProbeAt: now.Add(time.Minute)})
	return p, err
}

func TestModelPauseDeletedWithFolder(t *testing.T) {
	s := openEntities(t)
	openPause(t, s, "oldest", time.Now())
	if _, _, err := s.DeleteGroup("g"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.ReadModelPause(t.Context(), "g"); err != nil || p != nil {
		t.Fatalf("pause after folder deletion: %+v %v", p, err)
	}
}

type pauseFaultStore struct {
	objstore.Store
	failRead, failWrite bool
}

func (s pauseFaultStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.failRead && q.Entity == ModelPauses {
		return nil, errors.New("pause read unavailable")
	}
	return s.Store.Query(ctx, q)
}

func (s pauseFaultStore) GuardedBatch(ctx context.Context, c []objstore.Check, w []objstore.Write) (bool, error) {
	if s.failWrite {
		return false, errors.New("claim unavailable")
	}
	return s.Store.GuardedBatch(ctx, c, w)
}

func TestModelPauseStoreErrorsHoldAndRefusalIsAtomic(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []pauseFaultStore{{Store: s.Obj(), failRead: true}, {Store: s.Obj(), failWrite: true}} {
		broken := NewOver(fault)
		if fault.failRead {
			if _, err := broken.ReadModelPause(t.Context(), "g"); err == nil {
				t.Fatal("read error swallowed")
			}
		} else if _, err := broken.OpenTurnContext("blocked", "g", "", "web:g", "u", "", nil); err == nil {
			t.Fatal("write error admitted turn")
		}
	}
	_, _ = s.OpenTurnContext("t", "g", "", "web:g", "u", "", nil)
	tc, _ := s.TurnContextOf("t")
	broken := NewOver(pauseFaultStore{Store: s.Obj(), failWrite: true})
	if _, err := recordPause(broken, tc, time.Now()); err == nil {
		t.Fatal("refusal write error swallowed")
	}
	if p, _ := s.ReadModelPause(t.Context(), "g"); p != nil {
		t.Fatal("pause landed without attempt")
	}
	if tc, _ := s.TurnContextOf("t"); tc.State != TurnRunning || tc.ProviderFailureAttempt != "" {
		t.Fatalf("attempt changed without pause: %+v", tc)
	}
}

type refusalBeforeRetire struct {
	objstore.Store
	fired bool
}

func (s *refusalBeforeRetire) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !s.fired {
		for _, write := range writes {
			if write.Entity == TurnContexts && write.Attrs[attrTurnState] == TurnDone {
				s.fired = true
				tc, _ := NewOver(s.Store).TurnContextOf("running")
				if _, err := recordPause(NewOver(s.Store), tc, time.Now()); err != nil {
					return false, err
				}
				break
			}
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestDeleteFolderRetriesRefusalAndKeepsCompletedTurn(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
		t.Fatal(err)
	}
	admission := &TurnAdmission{AttemptID: "attempt", InputLast: MsgStamp(core.NowTS(), "saved-span")}
	if _, err := s.OpenTurnContext("running", "g", "", "web:g", "u", "", admission); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTurnContext("completed", "g", "", "web:g", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnState("completed", TurnDone); err != nil {
		t.Fatal(err)
	}
	before, _ := s.TurnContextOf("completed")
	gate := &refusalBeforeRetire{Store: s.Obj()}
	if _, _, err := NewOver(gate).DeleteGroup("g"); err != nil {
		t.Fatal(err)
	}
	turn, ok := s.TurnContextOf("running")
	cursor, cursorErr := s.AgentCursor("web:g")
	if !gate.fired || !ok || turn.State != TurnDone || !turn.RunReturned || cursorErr != nil || cursor != admission.InputLast {
		t.Fatalf("refusal survived retirement: fired=%v turn=%+v cursor=%q", gate.fired, turn, cursor)
	}
	after, ok := s.TurnContextOf("completed")
	if !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("completed history changed: before=%+v after=%+v", before, after)
	}
}

type deleteBeforeRefusal struct {
	objstore.Store
	fired bool
}

func (s *deleteBeforeRefusal) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !s.fired && len(writes) == 3 && writes[0].Entity == ModelPauses && writes[2].Entity == TurnContexts {
		s.fired = true
		if _, _, err := NewOver(s.Store).DeleteGroup("g"); err != nil {
			return false, err
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestDeleteFolderDefeatsInFlightRefusal(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
		t.Fatal(err)
	}
	admission := &TurnAdmission{AttemptID: "attempt", InputLast: MsgStamp(core.NowTS(), "saved-span")}
	if _, err := s.OpenTurnContext("running", "g", "", "web:g", "u", "", admission); err != nil {
		t.Fatal(err)
	}
	gate := &deleteBeforeRefusal{Store: s.Obj()}
	tc, _ := s.TurnContextOf("running")
	if _, err := recordPause(NewOver(gate), tc, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn, ok := s.TurnContextOf("running")
	cursor, err := s.AgentCursor("web:g")
	if !gate.fired || !ok || turn.State != TurnDone || !turn.RunReturned || err != nil || cursor != admission.InputLast {
		t.Fatalf("stale refusal changed retired turn: fired=%v turn=%+v cursor=%q err=%v", gate.fired, turn, cursor, err)
	}
	if pause, err := s.ReadModelPause(t.Context(), "g"); err != nil || pause != nil {
		t.Fatalf("stale refusal restored pause: %+v %v", pause, err)
	}
}

type deleteGroupOnAdmission struct {
	objstore.Store
	op    objstore.WriteOp
	fired bool
}

func (s *deleteGroupOnAdmission) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if !s.fired && w.Entity == TurnContexts && w.Op == s.op {
			s.fired = true
			_, err := s.Store.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: Groups, Key: objstore.Key{Partition: OwnerRoutd, Sort: "g"}}})
			if err != nil {
				return false, err
			}
			break
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

// Deleting the group before a rebind or probe must refuse the whole batch.
func TestAdmissionRequiresGroupDuringRebindAndProbe(t *testing.T) {
	for _, tc := range []struct {
		name          string
		probe, rebind bool
	}{
		{name: "rebind", rebind: true},
		{name: "probe fresh", probe: true},
		{name: "probe rebind", probe: true, rebind: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openEntities(t)
			if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
				t.Fatal(err)
			}
			if tc.rebind {
				if live, err := s.OpenTurnContext("target", "g", "old", "web:g", "u", "", nil); err != nil || !live {
					t.Fatalf("initial turn: %v %v", live, err)
				}
			}
			var admission *TurnAdmission
			if tc.probe {
				p := openPause(t, s, "pause", time.Now().Add(-2*time.Minute))
				admission = &TurnAdmission{ProbeEpisode: p.OpenedAt}
			}
			op := objstore.Put
			if tc.rebind {
				op = objstore.Update
			}
			gate := &deleteGroupOnAdmission{Store: s.Obj(), op: op}
			guarded := NewOver(gate)
			live, err := guarded.OpenTurnContext("target", "g", "new", "web:g", "u", "", admission)
			if !gate.fired || live || !errors.Is(err, ErrGroupMissing) {
				t.Fatalf("admission: fired=%v live=%v err=%v", gate.fired, live, err)
			}
			turn, exists := s.TurnContextOf("target")
			if exists != tc.rebind || (exists && turn.Topic != "old") {
				t.Fatalf("turn changed: %+v exists=%v", turn, exists)
			}
			if tc.probe {
				p, err := s.ReadModelPause(t.Context(), "g")
				if err != nil || p == nil || p.ProbeTurnID != "" {
					t.Fatalf("probe claimed: %+v err=%v", p, err)
				}
			}
		})
	}
}

func TestAdmissionMissingGroup(t *testing.T) {
	s := openEntities(t)
	for _, admission := range []*TurnAdmission{nil, {ProbeEpisode: "missing"}} {
		live, err := s.OpenTurnContext("target", "g", "", "web:g", "u", "", admission)
		if live || !errors.Is(err, ErrGroupMissing) {
			t.Fatalf("admission: live=%v err=%v", live, err)
		}
	}
	if _, exists := s.TurnContextOf("target"); exists {
		t.Fatal("turn created for missing group")
	}
	if p, err := s.ReadModelPause(t.Context(), "g"); err != nil || p != nil {
		t.Fatalf("pause: %+v err=%v", p, err)
	}
}

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
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
	p, err := s.RecordProviderFailure(t.Context(), id, tc.AttemptID, "rate_limit", time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModelPauseAdmissionAndSingleProbe(t *testing.T) {
	s := openEntities(t)
	now := time.Now().Add(-time.Minute)
	p := openPause(t, s, "oldest", now)
	if p.Attempts != 1 || p.NextAttemptAt < core.TS(now.Add(30*time.Second)) ||
		p.NextAttemptAt > core.TS(now.Add(33*time.Second)) {
		t.Fatalf("first pause: %+v", p)
	}
	for _, id := range []string{"fresh", "oldest"} {
		if _, err := s.OpenTurnContext(id, "g", "", "web:g", "u", "", nil); !errors.Is(err, ErrModelPaused) {
			t.Fatalf("normal fresh/rebind claim admitted during pause: %s %v", id, err)
		}
	}
	var wg sync.WaitGroup
	winners := make(chan string, 8)
	for i := range 8 {
		wg.Go(func() {
			id := fmt.Sprintf("probe-%d", i)
			live, err := s.OpenTurnContext(id, "g", "", "web:g", "u", "",
				&TurnAdmission{ProbeEpisode: p.OpenedAt})
			if err == nil && live {
				winners <- id
			} else if !errors.Is(err, ErrModelPaused) {
				t.Errorf("probe: %v", err)
			}
		})
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("probe winners=%d, want one", len(winners))
	}
	probe, err := s.ReadModelPause(t.Context(), "g")
	if err != nil || probe.NextAttemptAt != "" || probe.ProbeAttemptID == "" {
		t.Fatalf("bound probe %+v %v", probe, err)
	}
	tc, _ := s.TurnContextOf(<-winners)
	if err := s.CompleteModelProbe(t.Context(), tc); err != nil {
		t.Fatal(err)
	}
	if p, err := s.ReadModelPause(t.Context(), "g"); err != nil || p != nil {
		t.Fatalf("probe did not clear: %+v %v", p, err)
	}
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

func TestModelPauseDuplicateBackoffResetAndLateCompletion(t *testing.T) {
	s := openEntities(t)
	now := time.Now().Add(-2 * time.Minute)
	p := openPause(t, s, "t", now)
	first, _ := s.TurnContextOf("t")
	dup, err := s.RecordProviderFailure(t.Context(), "t", first.AttemptID, "rate_limit", now.Add(time.Hour), now)
	if err != nil || *dup != *p {
		t.Fatalf("duplicate extended pause: %+v %+v %v", p, dup, err)
	}
	if _, err := s.OpenTurnContext("t", "g", "", "web:g", "u", "", &TurnAdmission{ProbeEpisode: p.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	probe, _ := s.TurnContextOf("t")
	second, err := s.RecordProviderFailure(t.Context(), "t", probe.AttemptID, "rate_limit", time.Time{}, now)
	if err != nil || second.Attempts != 2 || second.NextAttemptAt < core.TS(now.Add(time.Minute)) ||
		second.NextAttemptAt > core.TS(now.Add(66*time.Second)) {
		t.Fatalf("second delay %+v %v", second, err)
	}
	if err := s.CompleteModelProbe(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.ReadModelPause(t.Context(), "g"); p == nil {
		t.Fatal("old probe cleared a rearmed episode")
	}
	if _, err := s.OpenTurnContext("t", "g", "", "web:g", "u", "", &TurnAdmission{ProbeEpisode: p.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.TurnContextOf("t")
	reset := now.Add(3 * time.Hour)
	third, err := s.RecordProviderFailure(t.Context(), "t", current.AttemptID, "spend_limit", reset, now)
	if err != nil || third.NextAttemptAt != core.TS(reset) || third.OpenedAt != p.OpenedAt {
		t.Fatalf("reset override %+v %v", third, err)
	}
	for _, n := range []int{1, 2, 20, 100} {
		at, _ := time.Parse(time.RFC3339Nano, modelRetryAt(now, n, now.Add(-time.Hour)))
		if at.Sub(now) < 30*time.Second || at.Sub(now) > time.Hour {
			t.Fatalf("unbounded delay at attempt %d: %s", n, at.Sub(now))
		}
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
	if _, err := broken.RecordProviderFailure(t.Context(), "t", tc.AttemptID, "outage", time.Time{}, time.Now()); err == nil {
		t.Fatal("refusal write error swallowed")
	}
	if p, _ := s.ReadModelPause(t.Context(), "g"); p != nil {
		t.Fatal("pause landed without attempt")
	}
	if tc, _ := s.TurnContextOf("t"); tc.State != TurnRunning || tc.ProviderFailureAttempt != "" {
		t.Fatalf("attempt changed without pause: %+v", tc)
	}
}

func TestModelPauseBackoffClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		for _, test := range []struct {
			attempts  int
			low, high time.Duration
		}{
			{1, 30 * time.Second, 33 * time.Second}, {2, time.Minute, 66 * time.Second},
			{100, time.Hour, time.Hour},
		} {
			at, err := time.Parse(time.RFC3339Nano, modelRetryAt(now, test.attempts, time.Time{}))
			if err != nil || at.Sub(now) < test.low || at.Sub(now) > test.high {
				t.Fatalf("attempt %d delay=%s err=%v", test.attempts, at.Sub(now), err)
			}
		}
		reset := now.Add(4 * time.Hour)
		if modelRetryAt(now, 2, reset) != core.TS(reset) {
			t.Fatal("reset did not override backoff")
		}
	})
}

func TestModelPauseLateProbeCannotClearNewEpisodeAndHeldSurvivesGC(t *testing.T) {
	s := openEntities(t)
	p := openPause(t, s, "old", time.Now().Add(-2*time.Minute))
	if _, err := s.OpenTurnContext("old", "g", "", "web:g", "u", "", &TurnAdmission{ProbeEpisode: p.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	old, _ := s.TurnContextOf("old")
	bound, _ := s.ReadModelPause(t.Context(), "g")
	ws := &WriteSet{}
	if err := s.ClearModelPauseInto(ws, *bound, errors.New("changed")); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	newer := openPause(t, s, "new", time.Now())
	if newer.OpenedAt == old.ProbeEpisode {
		t.Fatal("new episode reused old identity")
	}
	if err := s.CompleteModelProbe(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if after, err := s.ReadModelPause(t.Context(), "g"); err != nil || after == nil || after.OpenedAt != newer.OpenedAt {
		t.Fatalf("late probe cleared newer episode: %+v %v", after, err)
	}
	if err := s.setTurnAttrs("new", map[string]string{AttrTurnStartedAt: core.TS(time.Now().Add(-24 * time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepExpiredRunning(time.Second); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("new"); tc.State != TurnHeld || tc.RetryCount != 0 {
		t.Fatalf("held input changed under GC: %+v", tc)
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
				if _, err := NewOver(s.Store).RecordProviderFailure(ctx, "running", "attempt", "outage", time.Time{}, time.Now()); err != nil {
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
	if !s.fired && len(writes) == 2 && writes[0].Entity == ModelPauses && writes[1].Entity == TurnContexts {
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
	if _, err := NewOver(gate).RecordProviderFailure(t.Context(), "running", "attempt", "outage", time.Time{}, time.Now()); err != nil {
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
				if err != nil || p == nil || p.NextAttemptAt == "" || p.ProbeTurnID != "" {
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

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestControlWorkKeepsConcurrentTransitions(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	open := TurnControlState{Phase: "open", Episode: "one", NextProbeAt: now.Add(time.Hour)}
	if ok, err := s.WriteTurnControl(t.Context(), nil, open); err != nil || !ok {
		t.Fatalf("open %v %v", ok, err)
	}
	work, err := s.DueWork(t.Context(), "control", now)
	if err != nil || len(work) != 1 {
		t.Fatalf("work %v %v", work, err)
	}
	if err := s.RescheduleWork(t.Context(), work[0], open.NextProbeAt); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if got, err := NewOver(c).DueWork(t.Context(), "control", now); err != nil || len(got) != 0 || c.rows != 0 {
		t.Fatalf("future %v rows=%d %v", got, c.rows, err)
	}
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: TurnContexts, Key: objstore.Key{Partition: OwnerRoutd, Sort: "returned"}, Attrs: map[string]string{attrTurnState: TurnRunning, attrTurnRunReturned: flagSet}}); err != nil {
		t.Fatal(err)
	}
	next := open
	next.Episode = "two"
	next.NextProbeAt = now.Add(2 * time.Hour)
	if ok, err := s.WriteTurnControl(t.Context(), &open, next); err != nil || !ok {
		t.Fatalf("update %v %v", ok, err)
	}
	if err := s.RescheduleWork(t.Context(), work[0], now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	got, err := NewOver(c).DueWork(t.Context(), "control", now)
	if err != nil || len(got) != 1 || c.rows != 2 {
		t.Fatalf("lost transition %v rows=%d err=%v", got, c.rows, err)
	}
}
func TestControlWorkWakesOnInputAndRouting(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for _, w := range []objstore.Write{
		{Op: objstore.Put, Entity: Chats, Key: objstore.Key{Partition: OwnerRoutd, Sort: "chat"}, Attrs: map[string]string{AttrChatAgentCursor: MsgStamp(core.TS(time.Now()), "one")}},
		{Op: objstore.Put, Entity: Routes, Key: objstore.Key{Partition: OwnerRoutd, Sort: "route"}, Attrs: map[string]string{"match": "platform=web", "target": "a"}},
		{Op: objstore.Put, Entity: Groups, Key: objstore.Key{Partition: OwnerRoutd, Sort: "a"}, Attrs: map[string]string{"name": "a"}},
	} {
		if err := s.WriteEntities(t.Context(), nil, w); err != nil {
			t.Fatal(err)
		}
		work, err := s.DueWork(t.Context(), "control", time.Now())
		if err != nil || len(work) != 1 {
			t.Fatalf("%s work=%v err=%v", w.Entity, work, err)
		}
		if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
			t.Fatal(err)
		}
	}
}

type controlWorkHydrate struct {
	objstore.Store
	hook func()
}

func (r *controlWorkHydrate) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == RoutdWork && r.hook != nil {
		fn := r.hook
		r.hook = nil
		fn()
	}
	return r.Store.Query(ctx, q)
}
func TestControlWorkChecksHydratedDeadline(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	if ok, err := s.WriteTurnControl(t.Context(), nil, TurnControlState{Phase: "closed"}); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	work, err := s.DueWork(t.Context(), "control", now)
	if err != nil || len(work) != 1 {
		t.Fatalf("work %v %v", work, err)
	}
	r := &controlWorkHydrate{Store: s.Obj(), hook: func() {
		if err := s.RescheduleWork(t.Context(), work[0], now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}}
	if got, err := NewOver(r).DueWork(t.Context(), "control", now); err != nil || len(got) != 0 {
		t.Fatalf("future hydration %v %v", got, err)
	}
}
func TestControlWorkMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	if ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: TurnControl, Key: turnControlKey(), Attrs: controlAttrs(TurnControlState{Phase: "open", NextProbeAt: time.Now().Add(time.Hour)})}}); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	work, err := s.DueWork(t.Context(), "control", time.Now())
	if err != nil || len(work) != 1 {
		t.Fatalf("migrate %v %v", work, err)
	}
	if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if err := NewOver(c).MigrateStateIndexes(t.Context()); err != nil || c.rows != 12 {
		t.Fatalf("markers %d %v", c.rows, err)
	}
	if work, err := s.DueWork(t.Context(), "control", time.Now()); err != nil || len(work) != 0 {
		t.Fatalf("repeat %v %v", work, err)
	}
}
func TestControlWorkCoalescesBatch(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	var writes []objstore.Write
	for i := range 40 {
		writes = append(writes, objstore.Write{Op: objstore.Put, Entity: Groups, Key: objstore.Key{Partition: OwnerRoutd, Sort: fmt.Sprint(i)}, Attrs: map[string]string{"name": "group"}})
	}
	if err := s.WriteEntities(t.Context(), nil, writes...); err != nil {
		t.Fatal(err)
	}
	work, err := s.DueWork(t.Context(), "control", time.Now())
	if err != nil || len(work) != 1 {
		t.Fatalf("coalesce %v %v", work, err)
	}
}

func TestInterruptTurnKeepsConcurrentCompletion(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	if err := s.PutGroup(core.Group{Folder: "a"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext("probe", "a", "", "web:a", "u", "", &TurnAdmission{AttemptID: "attempt"}); err != nil || !ok {
		t.Fatalf("turn %v %v", ok, err)
	}
	tc, _, err := s.ReadTurnContext(t.Context(), "probe")
	if err != nil {
		t.Fatal(err)
	}
	r := &onboardingRace{Store: s.Obj(), fire: func(context.Context) error { return s.SetTurnState("probe", TurnDone) }}
	ok, err := NewOver(r).InterruptTurn(t.Context(), tc, nil, TurnControlState{Phase: "probe", ProbeTurnID: tc.TurnID, ProbeAttemptID: tc.AttemptID})
	if err != nil || ok {
		t.Fatalf("stale interruption %v %v", ok, err)
	}
	if _, exists, err := s.ReadTurnControl(t.Context()); err != nil || exists {
		t.Fatalf("completion reopened %v %v", exists, err)
	}
}

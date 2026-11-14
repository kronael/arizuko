package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestControlWorkReadsOnlyDueState(t *testing.T) {
	db, l, c := countedLoop(t)
	now := time.Now()
	l.q.now = func() time.Time { return now }
	open := store.TurnControlState{Phase: "open", Episode: "test", NextProbeAt: now.Add(time.Hour)}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, open); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	l.q.tick(l)
	clear(c.reads)
	l.q.tick(l)
	if n := c.count(store.TurnControl); n != 0 {
		t.Fatalf("future breaker reads=%d", n)
	}
}

type controlReadRace struct {
	objstore.Store
	hook func()
}

func (r *controlReadRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := r.Store.Query(ctx, q)
	if q.Entity == store.TurnControl && r.hook != nil {
		hook := r.hook
		r.hook = nil
		hook()
	}
	return items, err
}
func archivedProbe(t *testing.T, db *DB) store.TurnControlState {
	t.Helper()
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: store.TurnArchive, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "probe"}, Attrs: map[string]string{"state": store.TurnDone, "folder": "a", "chat_jid": "web:a", "attempt_id": "attempt", "run_returned": "1", "started_at": core.TS(time.Now())}}); err != nil {
		t.Fatal(err)
	}
	state := store.TurnControlState{Phase: "probe", Episode: "one", ProbeTurnID: "probe", ProbeAttemptID: "attempt", NextProbeAt: time.Now().Add(time.Hour)}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, state); err != nil || !ok {
		t.Fatalf("probe %v %v", ok, err)
	}
	return state
}
func TestControlWorkDoesNotReopenCompletedProbe(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	probe := archivedProbe(t, db)
	db.obj = &controlReadRace{Store: db.obj, hook: func() {
		if ok, err := db.store().WriteTurnControl(t.Context(), &probe, store.TurnControlState{Phase: "closed", Episode: probe.Episode}); err != nil || !ok {
			t.Fatalf("close %v %v", ok, err)
		}
	}}
	_ = l.q.recover(l, false)
	state, _, err := db.store().ReadTurnControl(t.Context())
	if err != nil || state.Phase != "closed" || p.stops != 0 {
		t.Fatalf("completed probe reopened: %+v stops=%d err=%v", state, p.stops, err)
	}
}

type failControlRearm struct {
	objstore.Store
	fail bool
}

func (r *failControlRearm) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if r.fail && w.Entity == store.TurnControl && w.Attrs["phase"] == "open" {
			r.fail = false
			return false, errors.New("rearm unavailable")
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestControlWorkRetriesArchivedProbe(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	archivedProbe(t, db)
	db.obj = &failControlRearm{Store: db.obj, fail: true}
	if err := l.q.recover(l, false); err == nil {
		t.Fatal("expected rearm failure")
	}
	state, _, _ := db.store().ReadTurnControl(t.Context())
	if state.Phase != "probe" {
		t.Fatalf("lost failed recovery %+v", state)
	}
	if err := l.q.recover(l, false); err != nil {
		t.Fatal(err)
	}
	state, _, _ = db.store().ReadTurnControl(t.Context())
	if state.Phase != "open" || p.stops != 2 {
		t.Fatalf("archived retry %+v stops=%d", state, p.stops)
	}
}
func TestControlWorkRetriesBusyProbeAtDeadline(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	archivedProbe(t, db)
	now := time.Now()
	l.q.now = func() time.Time { return now }
	l.q.activeFolders["a"] = "busy"
	if err := l.q.recover(l, false); err != nil {
		t.Fatal(err)
	}
	if work, err := db.store().DueWork(t.Context(), "control", now); err != nil || len(work) != 0 {
		t.Fatalf("busy not deferred %v %v", work, err)
	}
	delete(l.q.activeFolders, "a")
	if err := l.q.recover(l, false); err != nil {
		t.Fatal(err)
	}
	if p.stops != 0 {
		t.Fatal("probe retried before deadline")
	}
	now = now.Add(30 * time.Second)
	if err := l.q.recover(l, false); err != nil {
		t.Fatal(err)
	}
	if p.stops != 1 {
		t.Fatalf("busy probe stranded stops=%d", p.stops)
	}
}
func TestControlWakeRetainsInFlightCandidate(t *testing.T) {
	db, l, _, _ := pauseFixture(t)
	l.q.shuttingDown = false
	defer l.StopQueue()
	closed := store.TurnControlState{Phase: "closed", Episode: "closed"}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, closed); err != nil || !ok {
		t.Fatalf("close %v %v", ok, err)
	}
	l.q.getGroup("web:a").active = true
	db.obj = &candidateReadHook{Store: db.obj, hook: func() {
		done := make(chan struct{})
		go func() { l.q.wakeRefused(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("control wake blocked on candidate")
			return
		}
		l.q.mu.Lock()
		again := l.q.getGroup("web:a").again
		l.q.mu.Unlock()
		if !again {
			t.Error("wake was lost before admission finished")
		}
	}}
	if _, err := l.processGroupMessages("web:a"); err != nil {
		t.Fatal(err)
	}
	l.q.mu.Lock()
	s := l.q.getGroup("web:a")
	s.active = false
	candidate := s.candidate
	l.q.mu.Unlock()
	if candidate {
		t.Fatal("completed candidate stayed registered")
	}
}

type candidateReadHook struct {
	objstore.Store
	hook func()
}

func (r *candidateReadHook) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Chats && r.hook != nil {
		fn := r.hook
		r.hook = nil
		fn()
	}
	return r.Store.Query(ctx, q)
}

type recoveryReadRace struct {
	objstore.Store
	hook func()
}

func (r *recoveryReadRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := r.Store.Query(ctx, q)
	if q.Entity == store.TurnContexts && len(items) > 0 && r.hook != nil {
		fn := r.hook
		r.hook = nil
		fn()
	}
	return items, err
}
func TestControlWorkRechecksHydratedTurn(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	if ok, err := db.store().OpenTurnContext("probe", "a", "", "web:a", "u", "", &store.TurnAdmission{AttemptID: "attempt"}); err != nil || !ok {
		t.Fatalf("turn %v %v", ok, err)
	}
	if err := db.SetRunReturned("probe"); err != nil {
		t.Fatal(err)
	}
	probe := store.TurnControlState{Phase: "probe", Episode: "one", ProbeTurnID: "probe", ProbeAttemptID: "attempt"}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, probe); err != nil || !ok {
		t.Fatalf("probe %v %v", ok, err)
	}
	db.obj = &recoveryReadRace{Store: db.obj, hook: func() {
		if err := db.SetTurnState("probe", store.TurnDone); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.store().WriteTurnControl(t.Context(), &probe, store.TurnControlState{Phase: "closed", Episode: "one"}); err != nil || !ok {
			t.Fatalf("close %v %v", ok, err)
		}
	}}
	_ = l.q.recover(l, false)
	state, _, err := db.store().ReadTurnControl(t.Context())
	if err != nil || state.Phase != "closed" || p.stops != 0 {
		t.Fatalf("stale turn reopened %+v stops=%d err=%v", state, p.stops, err)
	}
}

func TestControlWorkRearmsUnownedProbe(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	tc := providerRefusal(t, db, p)
	before, _, err := db.store().ReadTurnControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	unowned := before
	unowned.ProbeTurnID = ""
	unowned.ProbeAttemptID = ""
	if ok, err := db.store().WriteTurnControl(t.Context(), &before, unowned); err != nil || !ok {
		t.Fatalf("unowned %v %v", ok, err)
	}
	if err := db.SetRunReturned(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.q.recover(l, false); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := db.store().ReadTurnControl(t.Context())
	if err != nil || after.Phase != "open" {
		t.Fatalf("unowned probe stranded %+v %v", after, err)
	}
}

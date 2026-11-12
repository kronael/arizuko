package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestScheduleTasks(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, task := range []core.Task{
		{ID: "once", Owner: "main", ChatJID: "web:main", Prompt: "hello", NextRun: &past, Status: core.TaskActive, ContextMode: "isolated"},
		{ID: "retry", Owner: "main", ChatJID: "web:main", NextRun: &past, Status: core.TaskActive},
		{ID: "cron", Owner: "main", ChatJID: "web:main", Cron: "0 9 * * *", NextRun: &past, Status: core.TaskActive},
	} {
		if err := store.NewOver(db.Obj()).CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	srv.fireTasks()
	id := "sched-once@" + past.Format(time.RFC3339)
	msg, ok := db.MessageByID("main", id)
	if !ok || msg.Sender != "timed-isolated:once" || msg.ForwardedFrom != "web:main" {
		t.Fatalf("message = %+v, found %v", msg, ok)
	}
	once, _ := db.GetTask("once")
	if once.Status != core.TaskCompleted {
		t.Fatalf("once = %+v", once)
	}
	retry, _ := db.GetTask("retry")
	if retry.Status != core.TaskActive || retry.NextRun == nil || !retry.NextRun.Equal(past) {
		t.Fatalf("retry = %+v", retry)
	}
	cron, _ := db.GetTask("cron")
	if cron.Status != core.TaskActive || cron.NextRun == nil || !cron.NextRun.After(time.Now()) {
		t.Fatalf("cron = %+v", cron)
	}
	cronLogs := store.NewOver(db.Obj()).TaskRunLogs("cron", 10)
	if len(cronLogs) != 1 || cronLogs[0].Status != "error" {
		t.Fatalf("cron failure logs = %+v", cronLogs)
	}
	onceLogs := store.NewOver(db.Obj()).TaskRunLogs("once", 10)
	if len(onceLogs) != 1 || onceLogs[0].Status != "success" {
		t.Fatalf("one-shot logs = %+v", onceLogs)
	}
	logs := store.NewOver(db.Obj()).TaskRunLogs("retry", 10)
	if len(logs) != 1 || logs[0].Status != "error" || logs[0].Error == "" {
		t.Fatalf("logs = %+v", logs)
	}
	if srv.schedule.Fired != 3 || srv.schedule.Err == "" || srv.schedule.LastTick.IsZero() {
		t.Fatalf("state = %+v", srv.schedule)
	}
}

func TestScheduleLoopStops(t *testing.T) {
	_, srv, _ := newTestRoutd(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.ScheduleLoop(ctx)
	if srv.schedule.LastTick.IsZero() {
		t.Fatal("startup tick missing")
	}
}

func TestScheduleRetryKeepsMessageIdentity(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	past := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	task := core.Task{ID: "retry-id", Owner: "main", ChatJID: "web:main", Prompt: "retry", NextRun: &past, Status: core.TaskActive}
	if err := store.NewOver(db.Obj()).CreateTask(task); err != nil {
		t.Fatal(err)
	}
	id := "sched-retry-id@" + past.Format(time.RFC3339)
	if err := db.PutMessage(core.Message{ID: id, ChatJID: "main", Sender: core.ScheduledSender, Content: "first", Timestamp: past}); err != nil {
		t.Fatal(err)
	}
	srv.fireTasks()
	msg, ok := db.MessageByID("main", id)
	if !ok || msg.Content != "first" {
		t.Fatalf("retry replaced original: %+v, found %v", msg, ok)
	}
	task, _ = db.GetTask(task.ID)
	if task.Status != core.TaskCompleted {
		t.Fatalf("task = %+v", task)
	}
}

// recoveryGate holds the recovery query until the test allows it to finish.
type recoveryGate struct {
	objstore.Store
	entered chan struct{}
	release chan struct{}
	tick    chan struct{}
}

func (g *recoveryGate) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.ScheduledTasks && len(q.Where) == 1 && q.Where[0].Attr == "status" {
		switch q.Where[0].Value {
		case store.TaskStatusFiring:
			close(g.entered)
			<-g.release
		case core.TaskActive:
			close(g.tick)
		}
	}
	return g.Store.Query(ctx, q)
}

func TestScheduleWaitsForRecovery(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	gate := &recoveryGate{Store: db.obj, entered: make(chan struct{}), release: make(chan struct{}), tick: make(chan struct{})}
	db.obj = gate
	loop := srv.loop
	loop.BindServer(srv)
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	<-gate.entered
	select {
	case <-gate.tick:
		t.Error("scheduler started before recovery finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate.release)
	select {
	case <-gate.tick:
	case <-time.After(5 * time.Second):
		t.Error("scheduler did not start after recovery")
	}
	cancel()
	if !loop.Wait(5 * time.Second) {
		t.Fatal("loop did not stop")
	}
}

// A router killed after a tick ingested a one-shot task's message but before
// it re-armed the task leaves the task 'firing'. The restarted router recovers
// it, fires it again under the same sched-<id>@<time> identity, keeps the
// first message and completes the task; a second restart fires nothing.
func TestScheduleRestartRefiresClaimedTask(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	past := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	task := core.Task{ID: "crash", Owner: "main", ChatJID: "web:main", Prompt: "again", NextRun: &past, Status: core.TaskActive}
	if err := store.NewOver(db.Obj()).CreateTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.DueTasks(time.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	id := "sched-crash@" + past.Format(time.RFC3339)
	if err := db.PutMessage(core.Message{ID: id, ChatJID: "main", Sender: core.ScheduledSender, Content: "first", Timestamp: past}); err != nil {
		t.Fatal(err)
	}
	restart := func() scheduleState {
		srv.scheduleMu.Lock()
		srv.schedule = scheduleState{}
		srv.scheduleMu.Unlock()
		loop := NewLoop(db, &stubRunner{t: t}, LoopConfig{})
		loop.StopQueue()
		loop.BindServer(srv)
		ctx, cancel := context.WithCancel(context.Background())
		go loop.Run(ctx)
		deadline := time.Now().Add(5 * time.Second)
		var state scheduleState
		for state.LastTick.IsZero() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			srv.scheduleMu.Lock()
			state = srv.schedule
			srv.scheduleMu.Unlock()
		}
		cancel()
		if !loop.Wait(5 * time.Second) {
			t.Fatal("loop did not stop")
		}
		return state
	}
	if state := restart(); state.Fired != 1 || state.Err != "" {
		t.Fatalf("first restart = %+v", state)
	}
	msgs, err := db.MessagesSince("main", "")
	if err != nil || len(msgs) != 1 || msgs[0].ID != id || msgs[0].Content != "first" {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	got, _ := db.GetTask(task.ID)
	if got.Status != core.TaskCompleted {
		t.Fatalf("task = %+v", got)
	}
	if state := restart(); state.Fired != 0 || state.LastTick.IsZero() {
		t.Fatalf("second restart = %+v", state)
	}
}

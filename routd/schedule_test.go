package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
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

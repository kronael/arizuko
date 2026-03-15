package store

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

func TestOpenMemAndMigrate(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestPutAndGetMessage(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	msg := core.Message{
		ID:        "m1",
		ChatJID:   "tg:123",
		Sender:    "alice",
		Name:      "Alice",
		Content:   "hello",
		Timestamp: now,
	}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	msgs, hi, err := s.NewMessages([]string{"tg:123"}, now.Add(-time.Second), "Bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("expected 'hello', got %q", msgs[0].Content)
	}
	if hi.Before(now) {
		t.Fatal("high-water mark should be >= message timestamp")
	}
}

func TestBotMessagesFiltered(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "user", Content: "hi",
		Timestamp: now,
	})
	s.PutMessage(core.Message{
		ID: "m2", ChatJID: "tg:1", Sender: "Andy", Content: "reply",
		Timestamp: now.Add(time.Second), BotMsg: true,
	})

	msgs, _, _ := s.NewMessages([]string{"tg:1"}, now.Add(-time.Second), "Andy")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 (bot filtered), got %d", len(msgs))
	}
}

func TestGroupCRUD(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	g := core.Group{
		JID:     "tg:123",
		Name:    "Test",
		Folder:  "main",
		AddedAt: time.Now(),
	}
	if err := s.PutGroup("tg:123", g); err != nil {
		t.Fatal(err)
	}

	got, ok := s.GetGroup("tg:123")
	if !ok {
		t.Fatal("group not found")
	}
	if got.Folder != "main" {
		t.Fatalf("expected folder 'main', got %q", got.Folder)
	}

	all := s.AllGroups()
	if len(all) != 1 {
		t.Fatalf("expected 1 group, got %d", len(all))
	}
}

func TestSessionState(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.SetSession("main", "sess-1")
	if got := s.GetSession("main"); got != "sess-1" {
		t.Fatalf("expected sess-1, got %q", got)
	}

	s.DeleteSession("main")
	if got := s.GetSession("main"); got != "" {
		t.Fatalf("expected empty after delete, got %q", got)
	}
}

func TestRouterState(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.SetState("cursor", "2024-01-01")
	if got := s.GetState("cursor"); got != "2024-01-01" {
		t.Fatalf("expected '2024-01-01', got %q", got)
	}
}

func TestSystemMessages(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.EnqueueSysMsg("main", "gateway", "new-session", "")
	s.EnqueueSysMsg("main", "gateway", "new-day", "")

	xml := s.FlushSysMsgs("main")
	if xml == "" {
		t.Fatal("expected non-empty XML")
	}

	// Second flush should be empty
	xml2 := s.FlushSysMsgs("main")
	if xml2 != "" {
		t.Fatalf("expected empty after flush, got %q", xml2)
	}
}

func TestChatErrorTracking(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.PutChat("tg:1", "Test", "telegram", false)
	if s.IsChatErrored("tg:1") {
		t.Fatal("should not be errored initially")
	}
	s.MarkChatErrored("tg:1")
	if !s.IsChatErrored("tg:1") {
		t.Fatal("should be errored after mark")
	}
	s.ClearChatErrored("tg:1")
	if s.IsChatErrored("tg:1") {
		t.Fatal("should not be errored after clear")
	}
}

func TestTaskCRUD(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	next := now.Add(time.Hour)
	task := core.Task{
		ID:      "t1",
		Owner:   "main",
		ChatJID: "tg:1",
		Prompt:  "check weather",
		Cron:    "0 9 * * *",
		NextRun: &next,
		Status:  "active",
		Created: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	got, ok := s.GetTask("t1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.Prompt != "check weather" {
		t.Fatalf("unexpected prompt: %q", got.Prompt)
	}
	if got.Cron != "0 9 * * *" {
		t.Fatalf("cron = %q, want '0 9 * * *'", got.Cron)
	}
	if got.Owner != "main" {
		t.Fatalf("owner = %q, want 'main'", got.Owner)
	}
	if got.Status != "active" {
		t.Fatalf("status = %q, want 'active'", got.Status)
	}
	if got.NextRun == nil {
		t.Fatal("next_run is nil")
	}

	// Update next_run and verify it changed
	past := now.Add(-time.Hour)
	if err := s.UpdateTask("t1", TaskPatch{NextRun: &past}); err != nil {
		t.Fatal("UpdateTask:", err)
	}
	got, ok = s.GetTask("t1")
	if !ok {
		t.Fatal("task not found after update")
	}
	if got.NextRun == nil || got.NextRun.After(now) {
		t.Fatal("next_run should be in the past after update")
	}
}

func TestTaskGetNonexistent(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, ok := s.GetTask("does-not-exist")
	if ok {
		t.Fatal("expected not found for nonexistent task")
	}
}

func TestTaskDuplicateID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	task := core.Task{
		ID: "dup-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "first", Status: "active", Created: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	task.Prompt = "second"
	err := s.CreateTask(task)
	if err == nil {
		t.Fatal("expected error on duplicate task ID")
	}
}

func TestTaskUpdateEmptyPatch(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.CreateTask(core.Task{
		ID: "ep-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "test", Status: "active", Created: now,
	})

	// empty patch should be a no-op
	err := s.UpdateTask("ep-1", TaskPatch{})
	if err != nil {
		t.Fatal("empty patch should not error:", err)
	}
	got, ok := s.GetTask("ep-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.Status != "active" {
		t.Fatalf("status changed after empty patch: %q", got.Status)
	}
}

func TestTaskUpdateNonexistent(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	status := "paused"
	// should not error, just no rows affected
	err := s.UpdateTask("ghost", TaskPatch{Status: &status})
	if err != nil {
		t.Fatal("UpdateTask on nonexistent should not error:", err)
	}
}

func TestTaskUpdateStatus(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.CreateTask(core.Task{
		ID: "us-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "test", Status: "active", Created: now,
	})

	status := "paused"
	s.UpdateTask("us-1", TaskPatch{Status: &status})
	got, _ := s.GetTask("us-1")
	if got.Status != "paused" {
		t.Fatalf("status = %q, want 'paused'", got.Status)
	}
}

func TestTaskDelete(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.CreateTask(core.Task{
		ID: "del-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "delete me", Status: "active", Created: now,
	})

	if err := s.DeleteTask("del-1"); err != nil {
		t.Fatal("DeleteTask:", err)
	}
	_, ok := s.GetTask("del-1")
	if ok {
		t.Fatal("task still exists after delete")
	}
}

func TestTaskDeleteNonexistent(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// deleting nonexistent should not error
	if err := s.DeleteTask("ghost"); err != nil {
		t.Fatal("DeleteTask on nonexistent should not error:", err)
	}
}

func TestTaskListByFolder(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.CreateTask(core.Task{
		ID: "lf-1", Owner: "alpha", ChatJID: "tg:1",
		Prompt: "alpha task", Status: "active", Created: now,
	})
	s.CreateTask(core.Task{
		ID: "lf-2", Owner: "beta", ChatJID: "tg:2",
		Prompt: "beta task", Status: "active", Created: now.Add(time.Second),
	})
	s.CreateTask(core.Task{
		ID: "lf-3", Owner: "alpha", ChatJID: "tg:3",
		Prompt: "alpha task 2", Status: "active", Created: now.Add(2 * time.Second),
	})

	// non-root: filter by folder
	alpha := s.ListTasks("alpha", false)
	if len(alpha) != 2 {
		t.Fatalf("expected 2 alpha tasks, got %d", len(alpha))
	}

	beta := s.ListTasks("beta", false)
	if len(beta) != 1 {
		t.Fatalf("expected 1 beta task, got %d", len(beta))
	}

	// root: all tasks
	all := s.ListTasks("", true)
	if len(all) != 3 {
		t.Fatalf("expected 3 total tasks, got %d", len(all))
	}
}

func TestTaskListEmpty(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	tasks := s.ListTasks("main", false)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}

	all := s.ListTasks("", true)
	if len(all) != 0 {
		t.Fatalf("expected 0 tasks (root), got %d", len(all))
	}
}

func TestTaskOneShotNoCron(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	next := now.Add(time.Hour)
	s.CreateTask(core.Task{
		ID: "os-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "run once", NextRun: &next,
		Status: "active", Created: now,
	})

	got, ok := s.GetTask("os-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.Cron != "" {
		t.Fatalf("cron should be empty for one-shot, got %q", got.Cron)
	}
}

package store

import (
	"testing"
	"time"

	"github.com/onvos/kanipi/core"
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
		JID:    "tg:123",
		Name:   "Test",
		Folder: "main",
		AddedAt: time.Now(),
		Rules: []core.RoutingRule{
			{Kind: "command", Match: "/code", Target: "main/code"},
		},
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
	if len(got.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got.Rules))
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

func TestTaskCRUD(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	next := now.Add(time.Hour)
	task := core.Task{
		ID:       "t1",
		Group:    "main",
		ChatJID:  "tg:1",
		Prompt:   "check weather",
		SchedTyp: "cron",
		SchedVal: "0 9 * * *",
		CtxMode:  "group",
		NextRun:  &next,
		Status:   "active",
		Created:  now,
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

	// Not due yet
	due, _ := s.DueTasks()
	if len(due) != 0 {
		t.Fatalf("expected 0 due tasks, got %d", len(due))
	}

	// Make it due
	past := now.Add(-time.Hour)
	s.UpdateTask("t1", TaskPatch{NextRun: &past})
	due, _ = s.DueTasks()
	if len(due) != 1 {
		t.Fatalf("expected 1 due task, got %d", len(due))
	}
}

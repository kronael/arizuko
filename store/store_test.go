package store

import (
	"strings"
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
		Name:    "Test",
		Folder:  "main",
		AddedAt: time.Now(),
	}
	if err := s.PutGroup(g); err != nil {
		t.Fatal(err)
	}

	all := s.AllGroups()
	if len(all) != 1 {
		t.Fatalf("expected 1 group, got %d", len(all))
	}
	got, ok := all["main"]
	if !ok {
		t.Fatal("group not found by folder key")
	}
	if got.Name != "Test" {
		t.Fatalf("expected name 'Test', got %q", got.Name)
	}
}

func TestSessionState(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.SetSession("main", "", "sess-1")
	if got, ok := s.GetSession("main", ""); !ok || got != "sess-1" {
		t.Fatalf("expected sess-1, got %q", got)
	}

	s.DeleteSession("main", "")
	if got, _ := s.GetSession("main", ""); got != "" {
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
	// Spec Y-system-messages: tag must be <system>, not <system_message>
	if !strings.Contains(xml, "<system ") {
		t.Errorf("expected <system> tag, got: %s", xml)
	}
	if strings.Contains(xml, "<system_message") {
		t.Errorf("unexpected <system_message> tag, got: %s", xml)
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

func TestUnroutedChatJIDs_NoMessages(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	jids := s.UnroutedChatJIDs(time.Now().Add(-time.Hour))
	if len(jids) != 0 {
		t.Fatalf("expected 0 jids, got %v", jids)
	}
}

func TestUnroutedChatJIDs_NoRoute(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "user",
		Content: "hi", Timestamp: now,
	})
	s.PutMessage(core.Message{
		ID: "m2", ChatJID: "tg:2", Sender: "user",
		Content: "hello", Timestamp: now,
	})

	jids := s.UnroutedChatJIDs(now.Add(-time.Second))
	if len(jids) != 2 {
		t.Fatalf("expected 2 unrouted jids, got %v", jids)
	}
}

func TestUnroutedChatJIDs_ExcludesRouted(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "user",
		Content: "hi", Timestamp: now,
	})
	s.PutMessage(core.Message{
		ID: "m2", ChatJID: "tg:2", Sender: "user",
		Content: "hello", Timestamp: now,
	})

	// route tg:1 → should be excluded
	s.AddRoute("tg:1", core.Route{Type: "default", Target: "main"})

	jids := s.UnroutedChatJIDs(now.Add(-time.Second))
	if len(jids) != 1 || jids[0] != "tg:2" {
		t.Fatalf("expected [tg:2], got %v", jids)
	}
}

func TestUnroutedChatJIDs_ExcludesBotMessages(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "bot",
		Content: "reply", Timestamp: now, BotMsg: true,
	})

	jids := s.UnroutedChatJIDs(now.Add(-time.Second))
	if len(jids) != 0 {
		t.Fatalf("bot messages should be excluded, got %v", jids)
	}
}

func TestPutMessageAttachmentsRoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	atts := `[{"mime":"image/jpeg","filename":"photo.jpg","url":"http://teled:9001/files/abc","size":1024}]`
	now := time.Now()
	if err := s.PutMessage(core.Message{
		ID: "att1", ChatJID: "tg:1", Sender: "user",
		Content: "look at this", Timestamp: now, Attachments: atts,
	}); err != nil {
		t.Fatal(err)
	}

	msgs, _, err := s.NewMessages([]string{"tg:1"}, now.Add(-time.Second), "Bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Attachments != atts {
		t.Errorf("attachments = %q, want %q", msgs[0].Attachments, atts)
	}
}

func TestEnrichMessage(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	atts := `[{"mime":"audio/ogg","filename":"voice.ogg","url":"http://teled:9001/files/xyz","size":512}]`
	now := time.Now()
	s.PutMessage(core.Message{
		ID: "enr1", ChatJID: "tg:1", Sender: "user",
		Content: "[Voice message]", Timestamp: now, Attachments: atts,
	})

	if err := s.EnrichMessage("enr1", "[Voice message]\n<attachment path=\"/groups/main/media/voice.ogg\" mime=\"audio/ogg\" filename=\"voice.ogg\" transcript=\"hello world\"/>"); err != nil {
		t.Fatal(err)
	}

	msgs, _, _ := s.NewMessages([]string{"tg:1"}, now.Add(-time.Second), "Bot")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Attachments != "" {
		t.Errorf("attachments should be cleared after enrich, got %q", msgs[0].Attachments)
	}
	if !strings.Contains(msgs[0].Content, "<attachment") {
		t.Errorf("enriched content should contain attachment XML, got %q", msgs[0].Content)
	}
}

func TestUnroutedChatJIDs_SinceFilter(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	old := time.Now().Add(-time.Hour)
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "user",
		Content: "old message", Timestamp: old,
	})

	// query since now — old message should not appear
	jids := s.UnroutedChatJIDs(time.Now())
	if len(jids) != 0 {
		t.Fatalf("expected 0 (message before since), got %v", jids)
	}
}

func TestStoreOutbound_EmptyPlatformMsgIDNoCollision(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// Two unsent outbound entries (empty PlatformMsgID) must both persist;
	// prior bug synthesized a single "out-" PK for every empty id.
	for i, body := range []string{"first", "second"} {
		err := s.StoreOutbound(core.OutboundEntry{
			ChatJID: "tg:1", Content: body,
			Source: "agent", GroupFolder: "main",
		})
		if err != nil {
			t.Fatalf("StoreOutbound #%d: %v", i, err)
		}
	}

	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE chat_jid = ? AND id LIKE 'out-unsent-%'`,
		"tg:1",
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 unsent rows, got %d", n)
	}
}

// Regression: StoreOutbound rows must be is_bot_message=1 so MessagesSince
// excludes them. A prior fix left the flag at 0, which fed the agent's own
// output back as inbound and caused a self-reply loop.
func TestStoreOutbound_ExcludedFromMessagesSince(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.StoreOutbound(core.OutboundEntry{
		ChatJID: "tg:1", Content: "bot reply",
		Source: "agent", GroupFolder: "main",
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.MessagesSince("tg:1", time.Time{}, "REDACTED")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("StoreOutbound row leaked into MessagesSince: %+v", msgs)
	}
}

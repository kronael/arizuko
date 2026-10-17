package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestOpenMem(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestPutAndGetMessage(t *testing.T) {
	s := openMem(t)
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

	msgs, err := s.NewMessagesForChat(context.Background(), "tg:123", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("expected 'hello', got %q", msgs[0].Content)
	}
	// The watermark a consumer records is the message's own key, so the next
	// read of `> cursor` starts past it rather than at the same instant.
	if cursor := MessageCursor(msgs[0]); cursor != MsgStamp(core.TS(now), "m1") {
		t.Fatalf("cursor = %q, want the message's own key", cursor)
	}
}

// A chat fed from its own watermark hands back only what landed after it.
func TestNewMessagesForChat_FeedsPastTheCursor(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	now := time.Now()
	first := core.Message{ID: "m1", ChatJID: "tg:1", Sender: "user", Content: "hi",
		Timestamp: now}
	second := core.Message{ID: "m2", ChatJID: "tg:1", Sender: "user", Content: "again",
		Timestamp: now.Add(time.Second)}
	for _, m := range []core.Message{first, second} {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := s.NewMessagesForChat(ctx, "tg:1", MessageCursor(first), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m2" {
		t.Fatalf("feed past the first message = %+v, want only m2", msgs)
	}

	// A LATE APPEND AT AN INSTANT THE CHAT HAS ALREADY USED takes the chat's
	// next position, so it lands above the tail instead of behind the cursor a
	// running turn is about to set — that burial lost eleven of twelve documents
	// of one Telegram upload (BUGS L103). AppendMessage answers the row as
	// stored, which is the only copy whose position is the real one.
	stored, err := s.AppendMessage(core.Message{ID: "m1-twin", ChatJID: "tg:1",
		Sender: "user", Content: "same instant", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err = s.NewMessagesForChat(ctx, "tg:1", MessageCursor(second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m1-twin" {
		t.Fatalf("feed past m2 = %+v, want the late twin", msgs)
	}

	// And it is consumed ONCE: a bare instant would sort below its own key and
	// hand the whole second back on every poll.
	msgs, err = s.NewMessagesForChat(ctx, "tg:1", MessageCursor(stored), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("feed past the twin = %+v, want nothing", msgs)
	}
}

// A RE-DELIVERY IS NOT A LATE ARRIVAL. The same id at the same instant is the
// same position, and the append-only log's ItemAbsent condition is the only
// dedup ingest has — routd mints `<adapter>-<idempotency-key>` for exactly this.
// A late append takes the chat's next position, so the re-delivery has to be
// told apart from a row that merely arrived out of order, or the platform's
// retry appends the message twice.
func TestPutMessage_RedeliveryIsStillANoOp(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	now := time.Now()
	first := core.Message{ID: "tg-7", ChatJID: "tg:1", Sender: "user",
		Content: "hi", Timestamp: now}
	if err := s.PutMessage(first); err != nil {
		t.Fatal(err)
	}
	// A second row lands after it, so the re-delivery below is no longer the
	// chat's newest position — which is what makes the append-order rule apply.
	if err := s.PutMessage(core.Message{ID: "tg-8", ChatJID: "tg:1", Sender: "user",
		Content: "again", Timestamp: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	redelivered := first
	redelivered.Content = "hi (retried)"
	if err := s.PutMessage(redelivered); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.NewMessagesForChat(ctx, "tg:1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("chat holds %d rows, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].ID != "tg-7" || msgs[0].Content != "hi" {
		t.Errorf("first row = %+v; the first write is authoritative", msgs[0])
	}
}

func TestGroupCRUD(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	g := core.Group{
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
	if _, ok := all["main"]; !ok {
		t.Fatal("group not found by folder key")
	}
}

func TestSessionState(t *testing.T) {
	s := openMem(t)
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

func TestTaskCRUD(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
	defer s.Close()

	_, ok := s.GetTask("does-not-exist")
	if ok {
		t.Fatal("expected not found for nonexistent task")
	}
}

func TestTaskDuplicateID(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	s.CreateTask(core.Task{
		ID: "ep-1", Owner: "main", ChatJID: "tg:1",
		Prompt: "test", Status: "active", Created: now,
	})

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

// TestTaskUpdateNonexistent: patching a task nobody created is an ERROR. The
// Update requires the item to exist, and this path writes an audit_log row
// saying `task.update` succeeded — so a swallowed refusal records a change that
// never happened (BUGS L268).
func TestTaskUpdateNonexistent(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	status := "paused"
	err := s.UpdateTask("ghost", TaskPatch{Status: &status})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("UpdateTask on a task that is not there answered %v, want ErrRefused", err)
	}
}

func TestTaskUpdateStatus(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
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
	s := openMem(t)
	defer s.Close()

	if err := s.DeleteTask("ghost"); err != nil {
		t.Fatal("DeleteTask on nonexistent should not error:", err)
	}
}

func TestTaskListByFolder(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
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
	s := openMem(t)
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

func TestPutMessageAttachmentsRoundTrip(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	atts := `[{"mime":"image/jpeg","filename":"photo.jpg","url":"http://teled:9001/files/abc","size":1024}]`
	now := time.Now()
	if err := s.PutMessage(core.Message{
		ID: "att1", ChatJID: "tg:1", Sender: "user",
		Content: "look at this", Timestamp: now, Attachments: atts,
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.NewMessagesForChat(context.Background(), "tg:1", "", 10)
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
	s := openMem(t)
	defer s.Close()

	atts := `[{"mime":"audio/ogg","filename":"voice.ogg","url":"http://teled:9001/files/xyz","size":512}]`
	now := time.Now()
	m := core.Message{
		ID: "enr1", ChatJID: "tg:1", Sender: "user",
		Content: "[Voice message]", Timestamp: now, Attachments: atts,
	}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}

	if err := s.EnrichMessage(m, "[Voice message]\n<attachment path=\"/groups/main/media/voice.ogg\" mime=\"audio/ogg\" filename=\"voice.ogg\" transcript=\"hello world\"/>"); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.NewMessagesForChat(context.Background(), "tg:1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
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

// Agent output with topic must appear in MessagesByTopic for web-chat history.
func TestAgentOutput_WritesTopicAndRoutedTo(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	if err := s.PutMessage(core.Message{
		ID: "out-t1", ChatJID: "web:main", Sender: "main",
		Content: "hi", Timestamp: time.Now(),
		FromMe: true, BotMsg: true, RoutedTo: "web:main",
		Topic: "t1",
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.MessagesByTopic("main", "t1", time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Topic != "t1" || msgs[0].RoutedTo != "web:main" {
		t.Fatalf("agent output missing topic/routed_to: %+v", msgs)
	}
}

// Inbound messages with empty topic must inherit the chat's sticky topic
// so processWebTopics groups them alongside bot replies under the same topic.
func TestPutMessage_InheritsStickyTopic(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	jid := "web:main"
	if err := s.SetStickyTopic(jid, "support"); err != nil {
		t.Fatal(err)
	}

	if err := s.PutMessage(core.Message{
		ID: "in-1", ChatJID: jid, Sender: "user",
		Content: "help", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.MessagesByTopic("main", "support", time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Topic != "support" {
		t.Fatalf("inbound did not inherit sticky topic: %+v", msgs)
	}
}

// MessagesByThread filters by both jid AND topic; messages from another chat
// or another thread on the same chat must not leak into the result.
func TestMessagesByThread_ScopedByJIDAndTopic(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	rows := []core.Message{
		{ID: "a", ChatJID: "telegram:group/-100", Sender: "u1", Content: "in t1",
			Timestamp: now.Add(-3 * time.Minute), Topic: "t1"},
		{ID: "b", ChatJID: "telegram:group/-100", Sender: "u1", Content: "in t2",
			Timestamp: now.Add(-2 * time.Minute), Topic: "t2"},
		{ID: "c", ChatJID: "telegram:group/-200", Sender: "u1", Content: "other chat t1",
			Timestamp: now.Add(-1 * time.Minute), Topic: "t1"},
	}
	for _, m := range rows {
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("put %s: %v", m.ID, err)
		}
	}

	got, err := s.MessagesByThread("telegram:group/-100", "t1", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("MessagesByThread mis-scoped: %+v", got)
	}
}

// `sent` is the ABSENCE of a delivery item, so a plain append owes nothing and
// a row that still owes one is what the retry sweep reads.
func TestPutMessage_OwesNoDelivery(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	now := time.Now()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "user",
		Content: "hi", Timestamp: now,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountPendingOutbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pending after a plain append = %d, want 0 (sent is the absence)", n)
	}
	dead, err := s.DeadLetters(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Errorf("dead letters after a plain append = %d, want 0", len(dead))
	}

	owed := core.Message{ID: "m2", ChatJID: "tg:1", Sender: "bot", Content: "reply",
		Timestamp: now.Add(time.Second), BotMsg: true}
	if err := s.PutOutbound(ctx, owed, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingOutbound(ctx, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "m2" {
		t.Fatalf("pending outbound = %+v, want only m2", pending)
	}
	if pending[0].Attempts != 0 {
		t.Errorf("attempts on a row that has not been retried = %d, want 0", pending[0].Attempts)
	}
}

// A status no sweep reads is refused rather than stored: a typo would park the
// row in a partition nothing drains.
func TestPutOutbound_RefusesAnUnknownStatus(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	err := s.PutOutbound(context.Background(), core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "bot", Content: "x", Timestamp: time.Now(),
	}, "queued")
	if err == nil {
		t.Fatal("an unrecognised delivery status was accepted")
	}
}

// Spec 5/G-engagement — engagement primitives.
func TestEngagement_SetAndCheck(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	if engagedNow(s, "jid1", "") {
		t.Fatal("missing row should be idle")
	}
	if err := s.BumpEngagement("jid1", "", "folderA", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("SetEngagement: %v", err)
	}
	if !engagedNow(s, "jid1", "") {
		t.Fatal("expected engaged after SetEngagement")
	}
	if err := s.BumpEngagement("jid1", "", "folderA", time.Time{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if engagedNow(s, "jid1", "") {
		t.Fatal("expected idle after clear")
	}
	if err := s.BumpEngagement("jid1", "", "folderA", now.Add(-time.Second)); err != nil {
		t.Fatalf("past: %v", err)
	}
	if engagedNow(s, "jid1", "") {
		t.Fatal("expected idle for past deadline")
	}
}

// SetLastReply preserves last_reply_id on empty replyID and records the
// folder in engaged_folder. BumpEngagement toggles engaged_until without
// stomping last_reply_id.
func TestEngagement_SetLastReplyAndBump(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()

	if err := s.SetLastReply("jid1", "", "rid-1", "folderA"); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpEngagement("jid1", "", "folderA", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := s.LastReplyID("jid1", ""); got != "rid-1" {
		t.Fatalf("last_reply_id = %q", got)
	}
	if !engagedNow(s, "jid1", "") {
		t.Fatal("expected engaged")
	}
	if err := s.BumpEngagement("jid1", "", "folderA", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := s.LastReplyID("jid1", ""); got != "rid-1" {
		t.Fatalf("last_reply_id should be preserved, got %q", got)
	}
	if engagedNow(s, "jid1", "") {
		t.Fatal("expected cleared")
	}
}

// The window names its folder with no prior reply: BumpEngagement writes both.
// A SetEngagement on a fresh (jid, topic) still surfaces the folder.
func TestEngagement_WindowNamesItsFolder(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()

	if e, live := s.Engagement("jid1", ""); live || e.Folder != "" {
		t.Fatalf("empty case = %q, live=%v", e.Folder, live)
	}

	if err := s.BumpEngagement("jid1", "", "folderA", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if e, live := s.Engagement("jid1", ""); !live || e.Folder != "folderA" {
		t.Fatalf("engaged folder (no prior reply) = %q live=%v, want folderA", e.Folder, live)
	}
}

// engagedNow reports whether (jid, topic) has a live window, which is what the
// three-argument IsEngaged this replaces asked with its own clock.
func engagedNow(s *Store, jid, topic string) bool {
	_, live := s.Engagement(jid, topic)
	return live
}

func TestStatusRow_FoundBeforeDeliveryAndRewritten(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	owed := core.Message{
		ID: "st1", ChatJID: "tg:1", Sender: "demo", Content: "⏳ one", Verb: "status",
		TurnID: "t1", BotMsg: true, FromMe: true, Timestamp: time.Now(),
	}
	if err := s.PutOutbound(context.Background(), owed, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LastStatusRow("tg:1", "t2"); ok {
		t.Fatal("another turn's status row was found")
	}
	row, ok := s.LastStatusRow("tg:1", "t1")
	if !ok || row.ID != "st1" || row.PlatformID != "" {
		t.Fatalf("an undelivered status row must be found: ok=%v row=%+v", ok, row)
	}
	if err := s.SetMessageContent(row, "⏳ two"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LastStatusRow("tg:1", "t1")
	if got.Content != "⏳ two" {
		t.Fatalf("content = %q, want %q", got.Content, "⏳ two")
	}
	feed, err := s.RecentActivity(context.Background(), "", 10)
	if err != nil || len(feed) != 1 || feed[0].Preview != "⏳ two" {
		t.Fatalf("feed preview not rewritten with the content: %+v err=%v", feed, err)
	}
	if err := s.SetMessageContent(core.Message{ID: "nobody", ChatJID: "tg:1", Timestamp: time.Now()}, "x"); err == nil {
		t.Fatal("rewriting a message nobody appended must be refused")
	}
}

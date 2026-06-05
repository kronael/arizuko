package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// --- Behavior 1: circuit-breaker prune + session reset --------------------
// Mirrors gateway/gateway_test.go:TestOnCircuitBreakerOpen_PrunesAndResetsSession.
// When the breaker trips, the chat's errored rows are pruned and the folder's
// root session is cleared so the next inbound spawns clean.

func TestOnCircuitBreakerOpen_PrunesAndResetsSession(t *testing.T) {
	db, loop, _ := recLoop(t)
	const jid = "telegram:42"
	const folder = "grp"
	_ = db.PutGroup(core.Group{Folder: folder})
	_ = db.PutSession(folder, "", "sess-abc")

	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u", Content: "boom", Timestamp: now})
	_ = db.PutMessage(core.Message{ID: "m2", ChatJID: jid, Sender: "u", Content: "fresh", Timestamp: now.Add(time.Second)})
	if err := db.MarkMessagesErrored([]string{"m1"}); err != nil {
		t.Fatal(err)
	}

	loop.onCircuitBreakerOpen(jid, folder)

	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m2" {
		t.Errorf("after prune want only m2 remaining, got %+v", msgs)
	}
	if id, _ := db.GetSession(folder, ""); id != "" {
		t.Errorf("session should be cleared, got %q", id)
	}
}

// errBreakerRunner returns one error outcome with the breaker tripped — the run
// that pushes a folder past the threshold (mirrors runed reporting BreakerOpen).
type errBreakerRunner struct{}

func (errBreakerRunner) Run(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeError, Error: "too many failures", BreakerOpen: true}, nil
}

// TestRunTurnBreakerOpen_PrunesErroredBatch drives the full dispatch path: an
// error+breaker outcome marks the trigger batch errored, then the breaker prunes
// it and clears the folder session (the wiring gated does via the queue's
// notify-error hook; routd applies it inline on the breaker-tripping run).
func TestRunTurnBreakerOpen_PrunesErroredBatch(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, errBreakerRunner{}, LoopConfig{})
	loop.StopQueue()

	const jid = "tg:7"
	const folder = "grp"
	_ = db.PutGroup(core.Group{Folder: folder})
	doSetRoutes(t, db, []core.Route{{Match: "platform=tg", Target: folder}})
	_ = db.PutSession(folder, "", "sess-live")
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "x1", ChatJID: jid, Sender: "u", Content: "hi", Timestamp: now, Verb: "message"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}

	// The errored trigger row is pruned and the session cleared.
	if rows, _ := db.MessagesSince(jid, ""); len(rows) != 0 {
		t.Errorf("errored batch not pruned on breaker open: %+v", rows)
	}
	if id, _ := db.GetSession(folder, ""); id != "" {
		t.Errorf("session not cleared on breaker open: %q", id)
	}
}

// --- Behavior 2: session-idle 2-day stale reset on spawn ------------------
// Mirrors gateway/session_idle_test.go {TestSessionIdleExpired,
// TestSpawnResetsStaleSession, TestSpawnKeepsFreshSession}.

func TestSessionIdleExpired(t *testing.T) {
	db, loop, _ := recLoop(t)
	const jid = "telegram:42"

	// No cursor yet: never expired (fresh chat, no session anyway).
	if loop.sessionIdleExpired(jid) {
		t.Error("expected false for empty cursor")
	}

	// Cursor 1h ago: fresh.
	_ = db.SetAgentCursor(jid, time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano))
	if loop.sessionIdleExpired(jid) {
		t.Error("expected false for 1h-old cursor")
	}

	// Cursor 3d ago: expired.
	_ = db.SetAgentCursor(jid, time.Now().Add(-3*24*time.Hour).UTC().Format(time.RFC3339Nano))
	if !loop.sessionIdleExpired(jid) {
		t.Error("expected true for 3d-old cursor")
	}

	// Just under the threshold: not expired (strict >).
	_ = db.SetAgentCursor(jid, time.Now().Add(-sessionIdleExpiry).Add(time.Second).UTC().Format(time.RFC3339Nano))
	if loop.sessionIdleExpired(jid) {
		t.Error("expected false for just-under-threshold cursor")
	}
}

// sidRunner captures the session_id routd ships to runed (the value the spawn
// resumes from) and echoes it back as the outcome session so the post-run state
// reflects what was dispatched.
type sidRunner struct{ gotSID string }

func (r *sidRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.gotSID = req.SessionID
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK, SessionID: req.SessionID}, nil
}

// TestSpawnResetsStaleSession: a 3-day-old cursor with a stored session gets the
// session cleared before the spawn reads it, so the run starts fresh (empty sid).
func TestSpawnResetsStaleSession(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rr := &sidRunner{}
	loop := NewLoop(db, rr, LoopConfig{})
	loop.StopQueue()

	const jid = "telegram:42"
	const folder = "main"
	_ = db.PutGroup(core.Group{Folder: folder})
	doSetRoutes(t, db, []core.Route{{Match: "platform=telegram", Target: folder}})
	_ = db.PutSession(folder, "", "sid-week-old")

	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "n1", ChatJID: jid, Sender: "u", Content: "hello", Timestamp: now, Verb: "message"})
	// Cursor predates the new message but is 3 days stale: the message is fed,
	// and the stale session is reset before the spawn reads it.
	_ = db.SetAgentCursor(jid, now.Add(-3*24*time.Hour).Format(time.RFC3339Nano))

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if rr.gotSID != "" {
		t.Errorf("stale session not reset: spawn resumed sid %q, want fresh", rr.gotSID)
	}
}

// TestSpawnKeepsFreshSession: an active chat resumes its stored session.
func TestSpawnKeepsFreshSession(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rr := &sidRunner{}
	loop := NewLoop(db, rr, LoopConfig{})
	loop.StopQueue()

	const jid = "telegram:42"
	const folder = "main"
	_ = db.PutGroup(core.Group{Folder: folder})
	doSetRoutes(t, db, []core.Route{{Match: "platform=telegram", Target: folder}})
	_ = db.PutSession(folder, "", "sid-active")

	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "f1", ChatJID: jid, Sender: "u", Content: "hello", Timestamp: now, Verb: "message"})
	_ = db.SetAgentCursor(jid, now.Add(-2*time.Hour).Format(time.RFC3339Nano))

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if rr.gotSID != "sid-active" {
		t.Errorf("fresh session not resumed: spawn got sid %q, want sid-active", rr.gotSID)
	}
}

// --- Behavior 3: strip <think> / emit <status> on the reply path ----------
// Mirrors gateway/gateway_test.go:TestMakeOutputCallback_StripsThinksAndStatus.

func TestAppendAndDeliver_StripsThinksAndStatus(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_, _ = db.PutTurnContext("t1", "grp", "", "telegram:1", "user-1", "")

	status, _, row := srv.appendAndDeliver("t1", "telegram:1",
		"<think>internal thought</think>Visible reply<status>Working on it</status>", "", true)
	if status != 200 {
		t.Fatalf("status=%d want 200", status)
	}
	if row == nil || row.Content != "Visible reply" {
		t.Fatalf("reply row content = %v, want \"Visible reply\"", row)
	}
	if len(dl.sends) != 2 {
		t.Fatalf("send count = %d, want 2 (status + reply): %+v", len(dl.sends), dl.sends)
	}
	if dl.sends[0].text != "⏳ Working on it" {
		t.Errorf("status text = %q, want %q", dl.sends[0].text, "⏳ Working on it")
	}
	if dl.sends[1].text != "Visible reply" {
		t.Errorf("reply text = %q, want %q", dl.sends[1].text, "Visible reply")
	}
}

// TestAppendAndDeliver_PureThinkSilent: a reply that is only a <think> block
// delivers nothing and persists no reply row (gated FormatOutbound → "").
func TestAppendAndDeliver_PureThinkSilent(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_, _ = db.PutTurnContext("t1", "grp", "", "telegram:1", "user-1", "")

	_, _, row := srv.appendAndDeliver("t1", "telegram:1", "<think>just thinking</think>", "", true)
	if row != nil {
		t.Errorf("pure-think reply persisted a row: %+v", row)
	}
	if len(dl.sends) != 0 {
		t.Errorf("pure-think reply delivered %d messages, want 0", len(dl.sends))
	}
}

// --- Behavior 4: muted-group send-disable (SEND_DISABLED_GROUPS) ----------
// Mirrors gateway/gateway_test.go:TestMakeOutputCallback_MutedGroup.

func TestAppendAndDeliver_MutedGroup(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	srv.SetDisabledGroups([]string{"grp"})
	_, _ = db.PutTurnContext("t1", "grp", "", "telegram:12345", "user-1", "")

	status, _, row := srv.appendAndDeliver("t1", "telegram:12345", "hello world", "", true)
	if status != 200 {
		t.Fatalf("status=%d want 200", status)
	}
	if len(dl.sends) != 0 {
		t.Errorf("muted group: channel.Send called %d times, want 0", len(dl.sends))
	}
	if row == nil {
		t.Fatal("muted group: reply row not returned (must persist)")
	}
	if row.Content != "hello world" {
		t.Errorf("Content = %q, want %q", row.Content, "hello world")
	}
	if !row.BotMsg {
		t.Error("BotMsg should be true for outbound")
	}
	if row.Sender != "grp" {
		t.Errorf("Sender = %q, want %q", row.Sender, "grp")
	}
	if row.RoutedTo != "grp" {
		t.Errorf("RoutedTo = %q, want %q", row.RoutedTo, "grp")
	}
	if row.Status != core.MessageStatusSent {
		t.Errorf("Status = %q, want sent (muted row must not stay pending)", row.Status)
	}
}

// --- Behavior 5: self-routed dispatch cannot re-echo ----------------------
// gated guarded a LocalChannel self-echo loop (sloth 2026-05-10). The split has
// no LocalChannel: outbound is a bot row (stripped from triggers) delivered via
// the Deliverer, and delegation (resolveTarget) refuses a self-target. This test
// documents/guards that a folder cannot re-enqueue an inbound to itself.

func TestSelfRoutedDispatchCannotReEcho(t *testing.T) {
	db, loop, _ := recLoop(t)
	const folder = "main"
	_ = db.PutGroup(core.Group{Folder: folder})

	// resolveTarget refuses every self-target shape: a reply routed back to self,
	// a sticky pinned to self, and a route resolving to self.
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "bot1", ChatJID: folder, Sender: folder, Content: "out",
		BotMsg: true, RoutedTo: folder, Timestamp: now})
	replyToSelf := core.Message{ID: "u1", ChatJID: folder, Sender: "u", Content: "thanks", ReplyToID: "bot1"}
	if tgt := loop.resolveTarget(folder, replyToSelf, folder); tgt != "" {
		t.Errorf("reply routed to self delegated to %q, want \"\" (no self-echo)", tgt)
	}
	_ = db.SetStickyGroup(folder, folder)
	if tgt := loop.resolveTarget(folder, core.Message{ChatJID: folder, Sender: "u"}, folder); tgt != "" {
		t.Errorf("sticky-to-self delegated to %q, want \"\"", tgt)
	}
	_ = db.SetStickyGroup(folder, "")
	doSetRoutes(t, db, []core.Route{{Match: "", Target: folder}})
	if tgt := loop.resolveTarget(folder, core.Message{ChatJID: folder, Sender: "u"}, folder); tgt != "" {
		t.Errorf("route-to-self delegated to %q, want \"\"", tgt)
	}

	// A bot reply on the folder's own JID is never fed back as a trigger:
	// processGroupMessages strips bot rows. Process the bot-only chat → no run.
	if had, err := loop.processGroupMessages(folder); err != nil || had {
		t.Errorf("bot-only chat triggered a run (had=%v err=%v); self-echo must be impossible", had, err)
	}
}

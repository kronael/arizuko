package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

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

	// The cursor is a message key, so the instant is its lead.
	cursorAt := func(t time.Time) string { return store.MsgStamp(core.TS(t), "m0") }

	// Cursor 1h ago: fresh.
	_ = db.SetAgentCursor(jid, cursorAt(time.Now().Add(-1*time.Hour)))
	if loop.sessionIdleExpired(jid) {
		t.Error("expected false for 1h-old cursor")
	}

	// Cursor 3d ago: expired.
	_ = db.SetAgentCursor(jid, cursorAt(time.Now().Add(-3*24*time.Hour)))
	if !loop.sessionIdleExpired(jid) {
		t.Error("expected true for 3d-old cursor")
	}

	// Just under the threshold: not expired (strict >).
	_ = db.SetAgentCursor(jid, cursorAt(time.Now().Add(-sessionIdleExpiry).Add(time.Second)))
	if loop.sessionIdleExpired(jid) {
		t.Error("expected false for just-under-threshold cursor")
	}
}

// sidRunner captures the session_id routd ships to runed, the value the spawn
// resumes from.
type sidRunner struct{ gotSID string }

func (r *sidRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.gotSID = req.SessionID
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK}, nil
}

// TestSpawnResetsStaleSession: a 3-day-old cursor with a stored session gets the
// session cleared before the spawn reads it, so the run starts under a fresh id.
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
	_ = db.SetAgentCursor(jid, store.MsgStamp(core.TS(now.Add(-3*24*time.Hour)), "m0"))

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if rr.gotSID == "" || rr.gotSID == "sid-week-old" {
		t.Errorf("stale session not reset: spawn resumed sid %q, want a fresh one", rr.gotSID)
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
	_ = db.SetAgentCursor(jid, core.TS(now.Add(-2*time.Hour)))

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	if rr.gotSID != "sid-active" {
		t.Errorf("fresh session not resumed: spawn got sid %q, want sid-active", rr.gotSID)
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

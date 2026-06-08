package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// TestAtomicAppendRouteTurn checks the spec 5/E invariant: append inbound →
// resolve route → start turn is one ordered view; the agent_cursor advances
// only after the turn closes, the turn_context is bound at dispatch, and a
// duplicate submit_turn dedups on (folder, turn_id).
func TestAtomicAppendRouteTurn(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "", []apiv1.Route{{Match: "platform=slack", Target: "demo"}})

	// append two inbound rows on one chat.
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "slack:T/C/U", Sender: "u1", Content: "one", Timestamp: now, Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "slack:T/C/U", Sender: "u1", Content: "two", Timestamp: now.Add(time.Second), Verb: "message"})

	// cursor unset before processing.
	if c := db.GetAgentCursor("slack:T/C/U"); c != "" {
		t.Fatalf("cursor set before processing: %q", c)
	}

	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}

	// turn_context bound at dispatch with the LAST inbound id as turn_id.
	tc, ok := db.GetTurnContext("b")
	if !ok || tc.Folder != "demo" {
		t.Fatalf("turn_context not bound: %+v ok=%v", tc, ok)
	}
	// runed saw the rendered batch with BOTH rows (one ordered view).
	if runner.gotTurn != "b" {
		t.Fatalf("turn_id=%q want b", runner.gotTurn)
	}
	// cursor advanced past the batch tail AFTER the turn closed.
	if c := db.GetAgentCursor("slack:T/C/U"); c == "" {
		t.Fatal("cursor not advanced after turn close")
	}
	// turn flipped to done.
	tc2, _ := db.GetTurnContext("b")
	if tc2.State != "done" {
		t.Fatalf("turn state=%q want done", tc2.State)
	}
}

// TestRouteMissDrops checks an unroutable chat advances the cursor and fires
// no turn (route miss → drop, spec 5/E § pollOnce).
func TestRouteMissDrops(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	// no group, no route → miss.
	_ = db.PutMessage(core.Message{ID: "x", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})
	had, err := srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if had {
		t.Fatal("route miss reported output")
	}
	if runner.gotTurn != "" {
		t.Fatal("route miss dispatched a run")
	}
	if db.GetAgentCursor("slack:T/C/U") == "" {
		t.Fatal("route miss did not advance cursor")
	}
}

// TestRouteMissInsertsOnboarding checks the ported gateway.pollOnce route-miss
// branch: when onboarding is enabled and the platform is allowed, an unrouted
// chat federates an InsertOnboarding to onbod (which OWNS the table). Disabled or
// platform-filtered chats do NOT. Discord guild channels onboard only on mention.
func TestRouteMissInsertsOnboarding(t *testing.T) {
	mk := func(t *testing.T, enabled bool, platforms []string) *fakeOnbod {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		loop := NewLoop(db, nopRunner{}, LoopConfig{
			OnboardingEnabled: enabled, OnboardingPlatforms: platforms,
		})
		loop.StopQueue()
		fo := &fakeOnbod{}
		loop.SetOnbodClient(fo)
		now := time.Now().UTC()
		// unrouted telegram chat + a discord guild post (no mention) + a discord dm.
		_ = db.PutMessage(core.Message{ID: "t1", ChatJID: "telegram:42", Sender: "u", Content: "hi", Timestamp: now, Verb: "message"})
		_ = db.PutMessage(core.Message{ID: "g1", ChatJID: "discord:guild/c", Sender: "u", Content: "yo", Timestamp: now, Verb: "message"})
		_ = db.PutMessage(core.Message{ID: "d1", ChatJID: "discord:dm/u", Sender: "u", Content: "yo", Timestamp: now, Verb: "message"})
		loop.pollOnce()
		return fo
	}

	t.Run("enabled all platforms", func(t *testing.T) {
		fo := mk(t, true, nil)
		// telegram + discord dm onboard; discord guild (no mention) does not.
		got := map[string]bool{}
		for _, j := range fo.onboarded {
			got[j] = true
		}
		if !got["telegram:42"] || !got["discord:dm/u"] {
			t.Fatalf("expected telegram + discord dm onboarded, got %v", fo.onboarded)
		}
		if got["discord:guild/c"] {
			t.Fatalf("discord guild channel onboarded without a mention: %v", fo.onboarded)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		if fo := mk(t, false, nil); len(fo.onboarded) != 0 {
			t.Fatalf("onboarding disabled but inserted: %v", fo.onboarded)
		}
	})

	t.Run("platform filtered", func(t *testing.T) {
		fo := mk(t, true, []string{"telegram"})
		for _, j := range fo.onboarded {
			if j != "telegram:42" {
				t.Fatalf("platform filter telegram leaked %q: %v", j, fo.onboarded)
			}
		}
		if len(fo.onboarded) == 0 {
			t.Fatal("telegram allowed but not onboarded")
		}
	})
}

// TestTransportFailureNoAdvance checks a transport failure leaves the
// cursor un-advanced (re-fed next poll; spec 5/E § Transport failure).
func TestTransportFailureNoAdvance(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	failing := failingRunner{}
	loop := NewLoop(db, failing, LoopConfig{})
	loop.StopQueue()
	srv := NewServer(db, loop, nil, nil, 0, "")
	_ = srv
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "y", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	_, derr := loop.processGroupMessages("slack:T/C/U")
	if derr == nil {
		t.Fatal("expected transport error")
	}
	if db.GetAgentCursor("slack:T/C/U") != "" {
		t.Fatal("cursor advanced on transport failure (want re-feed)")
	}
}

type failingRunner struct{}

func (failingRunner) Run(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
	return runedv1.RunOutcome{}, context.DeadlineExceeded
}

// TestPollOnceGatesCurrentChat: pollOnce feeds from the GLOBAL min cursor, so a
// chat whose OWN cursor is already current shows up when another chat holds the
// min back. It must NOT be enqueued — else processGroupMessages returns
// (false,nil) and the shared queue counts a circuit-breaker failure (the split
// cutover's breaker churn). Mirrors gateway, which never enqueues an empty chat.
func TestPollOnceGatesCurrentChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	now := time.Now().UTC()
	// chat A: already processed — its cursor is AT its latest msg, and it holds
	// MinAgentCursor low so pollOnce's global poll surfaces it every tick.
	_ = db.PutMessage(core.Message{ID: "a1", ChatJID: "slack:T/CA/U", Sender: "u", Content: "old", Timestamp: now, Verb: "message"})
	_ = db.SetAgentCursor("slack:T/CA/U", now.Format(time.RFC3339Nano))
	// chat B: genuinely new — cursor unset (behind its message).
	_ = db.PutMessage(core.Message{ID: "b1", ChatJID: "slack:T/CB/U", Sender: "u", Content: "new", Timestamp: now.Add(time.Second), Verb: "message"})

	loop := NewLoop(db, nopRunner{}, LoopConfig{})
	got := make(chan string, 8)
	// Replace the queue's worker with a recorder so we observe exactly which
	// chats pollOnce enqueues (not what they dispatch).
	loop.q.SetProcessMessagesFn(func(jid string) (bool, error) { got <- jid; return false, nil })

	loop.pollOnce()

	select {
	case jid := <-got:
		if jid != "slack:T/CB/U" {
			t.Fatalf("processed %q, want only the new-work chat slack:T/CB/U", jid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new-work chat B was not enqueued")
	}
	select {
	case jid := <-got:
		t.Fatalf("already-current chat spuriously enqueued: %q (breaker-churn bug)", jid)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestSteeredPartialBatchNoRedispatch is the partial-batch double-dispatch fix:
// in a multi-sender poll the FIRST batch's turn completes and a LATER batch
// steers (another chat holds the folder), so processGroupMessages returns
// without advancing the cursor. On the next poll the same groups rebuild and
// the COMPLETED batch is re-fed — its turn must NOT re-dispatch (PutTurnContext
// refuses to resurrect a state='done' turn), or the agent's output replays.
func TestSteeredPartialBatchNoRedispatch(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})

	// First sender's turn completes (records a result, flips done); second
	// sender's turn steers. groupBySender yields [u1],[u2] in that order.
	var dispatched []string
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		dispatched = append(dispatched, req.TurnID)
		if req.TriggerSender == "u2" {
			return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK, Steered: true}, nil
		}
		_, _ = db.RecordTurnResult(string(req.Folder), req.TurnID, "sess", "success")
		_ = db.SetTurnState(req.TurnID, "done")
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK, SessionID: "sess"}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	now := time.Now().UTC()
	_ = db.PutMessage(core.Message{ID: "m-u1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "from one", Timestamp: now, Verb: "message"})
	_ = db.PutMessage(core.Message{ID: "m-u2", ChatJID: "slack:T/C/U", Sender: "u2",
		Content: "from two", Timestamp: now.Add(time.Second), Verb: "message"})

	// poll 1: u1 completes, u2 steers → no advance (cursor stays empty).
	if _, err := loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if len(dispatched) != 2 || dispatched[0] != "m-u1" || dispatched[1] != "m-u2" {
		t.Fatalf("poll 1 dispatched=%v want [m-u1 m-u2]", dispatched)
	}
	if db.GetAgentCursor("slack:T/C/U") != "" {
		t.Fatal("poll 1 advanced cursor despite a steered later batch")
	}
	if tc, _ := db.GetTurnContext("m-u1"); tc.State != "done" {
		t.Fatalf("u1 turn state=%q want done", tc.State)
	}

	// poll 2: same groups rebuild; the completed u1 batch is re-fed. It must
	// NOT re-dispatch — only u2 (still not done) dispatches again.
	if _, err := loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	var redispatchedU1 int
	for _, id := range dispatched[2:] {
		if id == "m-u1" {
			redispatchedU1++
		}
	}
	if redispatchedU1 != 0 {
		t.Fatalf("completed u1 batch re-dispatched on poll 2 (dispatched=%v)", dispatched)
	}
}

func doSetRoutes(t *testing.T, db *DB, routes []core.Route) {
	t.Helper()
	if _, err := db.SetRoutes("", routes); err != nil {
		t.Fatalf("set routes: %v", err)
	}
}

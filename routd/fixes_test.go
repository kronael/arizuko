package routd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// recDeliverer records every egress call and optionally fails Send.
type recDeliverer struct {
	sends    []sentMsg
	reacts   int
	failSend bool
	pid      string
}

type sentMsg struct {
	jid, text, replyTo, threadID, idem string
}

func (d *recDeliverer) Send(jid, text, replyTo, threadID, idem string) (string, error) {
	d.sends = append(d.sends, sentMsg{jid, text, replyTo, threadID, idem})
	if d.failSend {
		return "", errSend
	}
	if d.pid == "" {
		return "pid-" + idem, nil
	}
	return d.pid, nil
}
func (d *recDeliverer) React(_, _, _ string) error { d.reacts++; return nil }
func (d *recDeliverer) Edit(_, _, _ string) error  { return nil }
func (d *recDeliverer) Delete(_, _ string) error   { return nil }
func (d *recDeliverer) Pin(_, _ string) error      { return nil }
func (d *recDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (d *recDeliverer) Document(_, _, _, _, _, _ string) (string, error) {
	return d.pid, nil
}

var errSend = errSendT("send failed")

type errSendT string

func (e errSendT) Error() string { return string(e) }

// TestEarlySubmitTurnKeepsCallbacksLive is the [blocker] fix: an early
// submit_turn flips state→done, but trailing reply/send the still-live run
// emits stay valid until POST /v1/runs returns (run_returned). Only after the
// run-response do late callbacks 409 turn_done (spec 5/E § Post-terminal
// callbacks).
func TestEarlySubmitTurnKeepsCallbacksLive(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_ = db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1")
	h := srv.Handler()

	// submit_turn arrives early (flips state→done, run_returned stays 0).
	doJSON(t, h, "POST", "/v1/turns/t1/result", "",
		apiv1.TurnResult{TurnID: "t1", SessionID: "sX", Status: "success"})
	if tc, _ := db.GetTurnContext("t1"); tc.State != "done" || tc.RunReturned {
		t.Fatalf("after early submit_turn: state=%q run_returned=%v (want done/false)", tc.State, tc.RunReturned)
	}

	// a trailing send BEFORE the run-response must still succeed.
	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/send", "k-send",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "trailing frame"})
	if rec.Code != 200 {
		t.Fatalf("trailing send after early submit_turn: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("trailing send did not append the bot row")
	}

	// the run returns → run_returned set; now a late callback 409s.
	_ = db.SetRunReturned("t1")
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/t1/send", "k-late",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "too late"})
	if rec2.Code != 409 {
		t.Fatalf("post-run-return callback: status=%d want 409", rec2.Code)
	}
}

// TestSubmitTurnSessionWinsOverRunResponse is the session_id-clobber fix: when
// submit_turn already recorded a session, the run-response's session_id does
// NOT overwrite it (spec 5/E § Completion reconciliation).
func TestSubmitTurnSessionWinsOverRunResponse(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})

	// submit_turn arrives first via the runner stub, recording session "fromsubmit".
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		_, _ = db.RecordTurnResult(string(req.Folder), req.TurnID, "fromsubmit", "success")
		_ = db.PutSession(string(req.Folder), req.Topic, "fromsubmit")
		_ = db.SetTurnState(req.TurnID, "done")
		// the run-response carries a DIFFERENT session_id (the backstop).
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK, SessionID: "fromrun"}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "z", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := db.SessionID("demo", ""); got != "fromsubmit" {
		t.Fatalf("session_id=%q want fromsubmit (run-response clobbered submit_turn)", got)
	}
}

// TestRunErrorMarksAndNotifies is the outcome:error fix: a clean
// outcome:error advances past the batch, marks the chat errored, and sends a
// failure notice — not a silent success (spec 5/E § outcome).
func TestRunErrorMarksAndNotifies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	dl := &recDeliverer{}
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: "boom"}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "e", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	had, perr := loop.processGroupMessages("slack:T/C/U")
	if perr != nil {
		t.Fatalf("process: %v", perr)
	}
	if had {
		t.Fatal("outcome:error reported output (want false)")
	}
	if db.GetAgentCursor("slack:T/C/U") == "" {
		t.Fatal("outcome:error did not advance cursor (starvation hazard)")
	}
	if len(dl.sends) != 1 || dl.sends[0].text != runFailureNotice {
		t.Fatalf("expected one failure notice, got %+v", dl.sends)
	}
	var errored int
	db.SQL().QueryRow("SELECT errored FROM chats WHERE jid=?", "slack:T/C/U").Scan(&errored)
	if errored != 1 {
		t.Fatal("chat not marked errored after outcome:error")
	}
	// run_returned set so a late callback 409s.
	if tc, _ := db.GetTurnContext("e"); !tc.RunReturned {
		t.Fatal("run_returned not set after run-response")
	}
}

// TestIngressIdempotencyKey is the POST /v1/messages key fix: a stable id +
// X-Idempotency-Key is ambiguous (400); a key with no id mints
// <adapter>-<key> (spec 5/E § POST /v1/messages key rules).
func TestIngressIdempotencyKey(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	h := srv.Handler()

	// both id and key → 400 ambiguous_idempotency.
	rec := doJSONKey(t, h, "POST", "/v1/messages", "kk",
		apiv1.Message{ID: "wamid.1", ChatJID: "slack:T/C/U", Content: "hi"})
	if rec.Code != 400 {
		t.Fatalf("ambiguous status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var e apiv1.Err
	json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "ambiguous_idempotency" {
		t.Fatalf("error=%q want ambiguous_idempotency", e.Error)
	}

	// key, no id → mints adapter-<key> (no verifier → "adapter").
	rec2 := doJSONKey(t, h, "POST", "/v1/messages", "kk",
		apiv1.Message{ChatJID: "slack:T/C/U", Content: "hi"})
	if rec2.Code != 200 {
		t.Fatalf("mint status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var ack apiv1.MessageAck
	json.Unmarshal(rec2.Body.Bytes(), &ack)
	if ack.ID != "adapter-kk" {
		t.Fatalf("minted id=%q want adapter-kk", ack.ID)
	}
	if !db.MessageExists("adapter-kk") {
		t.Fatal("minted row not stored")
	}
}

// TestReactionTopicInheritance is the reaction topic-inheritance fix: a
// reaction/reply with no topic inherits the parent's topic so it routes to the
// parent's thread (spec 5/E § Channel ingress).
func TestReactionTopicInheritance(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	h := srv.Handler()

	// a parent message on topic "deploy".
	doJSON(t, h, "POST", "/v1/messages", "",
		apiv1.Message{ID: "parent", ChatJID: "slack:T/C/U", Sender: "u1", Content: "ship it", Topic: "deploy"})
	// a reaction replying to it, carrying no topic.
	doJSON(t, h, "POST", "/v1/messages", "",
		apiv1.Message{ID: "react", ChatJID: "slack:T/C/U", Sender: "u2", Content: "👍",
			Verb: "like", ReplyTo: "parent"})

	var topic string
	db.SQL().QueryRow("SELECT topic FROM messages WHERE id='react'").Scan(&topic)
	if topic != "deploy" {
		t.Fatalf("reaction topic=%q want deploy (inherited from parent)", topic)
	}
}

// TestOutboundRetryRedispatchesAndFails is the outboundRetryLoop fix: a
// pending bot row older than 30s is re-dispatched (adapter dedups on the
// stable id); one older than 24h is failed (spec 5/E § Outbound is
// poll-reconciled).
func TestOutboundRetryRedispatchesAndFails(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{pid: "pid-x"}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl})

	now := time.Now().UTC()
	// pending for 2 min → re-dispatched.
	_ = db.PutMessage(core.Message{ID: "stale", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "retry me", Timestamp: now.Add(-2 * time.Minute), BotMsg: true,
		Status: core.MessageStatusPending})
	// pending for 25h → failed, never re-dispatched.
	_ = db.PutMessage(core.Message{ID: "ancient", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "give up", Timestamp: now.Add(-25 * time.Hour), BotMsg: true,
		Status: core.MessageStatusPending})
	// fresh (5s) → left alone.
	_ = db.PutMessage(core.Message{ID: "fresh", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "wait", Timestamp: now.Add(-5 * time.Second), BotMsg: true,
		Status: core.MessageStatusPending})

	loop.nextRetryAt = now.Add(-time.Second) // due now
	loop.maybeRetryOutbound(now)

	if len(dl.sends) != 1 || dl.sends[0].idem != "stale" {
		t.Fatalf("expected one re-dispatch of 'stale', got %+v", dl.sends)
	}
	if st := statusOf(db, "stale"); st != core.MessageStatusSent {
		t.Fatalf("stale status=%q want sent (re-delivered)", st)
	}
	if st := statusOf(db, "ancient"); st != core.MessageStatusFailed {
		t.Fatalf("ancient status=%q want failed (>24h)", st)
	}
	if st := statusOf(db, "fresh"); st != core.MessageStatusPending {
		t.Fatalf("fresh status=%q want still pending (<30s)", st)
	}
}

// TestSweepExpiredRunning is the expired-sweep fix: a stale state='running'
// turn older than the run timeout flips to the DISTINCT 'expired' (not
// 'done'), so it stops being re-fed by crash recovery yet does not trip the
// done-guard against a legitimate re-dispatch (spec 5/E § turn lifecycle).
func TestSweepExpiredRunning(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a turn started 2h ago, still running.
	_ = db.PutTurnContext("old", "demo", "", "slack:T/C/U", "u1")
	db.SQL().Exec("UPDATE turn_context SET started_at=? WHERE turn_id='old'",
		time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano))
	// a turn started just now, still running.
	_ = db.PutTurnContext("new", "demo", "", "slack:T/C/U2", "u1")

	n, err := db.SweepExpiredRunning(time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d want 1", n)
	}
	if tc, _ := db.GetTurnContext("old"); tc.State != "expired" {
		t.Fatalf("old turn state=%q want expired", tc.State)
	}
	if tc, _ := db.GetTurnContext("new"); tc.State != "running" {
		t.Fatalf("new turn state=%q want running (under timeout)", tc.State)
	}
	// expired is neither re-fed (not running) nor a done-guard hit.
	running, _ := db.RunningTurns()
	for _, tc := range running {
		if tc.TurnID == "old" {
			t.Fatal("expired turn still listed in RunningTurns (would be re-fed)")
		}
	}
}

// TestSweepIdempotencyDropsExpired is the SweepIdempotency-invoked fix: the
// hourly GC drops ledger rows past expires_at so the ledger doesn't grow
// unbounded (spec 5/E § Idempotency step 4).
func TestSweepIdempotencyDropsExpired(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a live row (expires in the future) and a stale row (expired).
	db.SQL().Exec(`INSERT INTO idempotency_keys(endpoint,key,request_hash,status,response,created_at,expires_at)
		VALUES('POST /v1/turns/reply','live','h',200,'{}',?,?)`,
		nowTS(), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	db.SQL().Exec(`INSERT INTO idempotency_keys(endpoint,key,request_hash,status,response,created_at,expires_at)
		VALUES('POST /v1/turns/reply','stale','h',200,'{}',?,?)`,
		nowTS(), time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))

	// maybeGC (loop-driven) invokes SweepIdempotency hourly.
	loop := NewLoop(db, runnerFn(nil), LoopConfig{})
	loop.nextGCAt = time.Now().Add(-time.Second) // due now
	loop.maybeGC(time.Now())

	var live, stale int
	db.SQL().QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key='live'").Scan(&live)
	db.SQL().QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key='stale'").Scan(&stale)
	if live != 1 || stale != 0 {
		t.Fatalf("after GC: live=%d stale=%d want 1/0", live, stale)
	}
}

// TestMutationDoneGuard is the done-guard fix: like/edit/delete/pin/unpin
// 409 after the run returns (spec 5/E § Post-terminal callbacks). Without the
// guard a late mutation would touch a platform message past the live run.
func TestMutationDoneGuard(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	h := srv.Handler()
	_ = db.PutTurnContext("t", "demo", "", "slack:T/C/U", "u1")

	// before run-return: like succeeds.
	rec := doJSON(t, h, "POST", "/v1/turns/t/like", "",
		apiv1.ReactionRequest{JID: "slack:T/C/U", PlatformID: "p1", Reaction: "👍"})
	if rec.Code != 200 {
		t.Fatalf("like before run-return: status=%d", rec.Code)
	}

	// after run-return: like 409s (callback closed).
	_ = db.SetRunReturned("t")
	rec2 := doJSON(t, h, "POST", "/v1/turns/t/like", "",
		apiv1.ReactionRequest{JID: "slack:T/C/U", PlatformID: "p2", Reaction: "👍"})
	if rec2.Code != 409 {
		t.Fatalf("like after run-return: status=%d want 409 body=%s", rec2.Code, rec2.Body.String())
	}
	if dl.reacts != 1 {
		t.Fatalf("reacts=%d want 1 (the post-close like must not fire)", dl.reacts)
	}
}

// TestAppendAndFinishAtomic is the idempotency-atomicity fix: AppendAndFinish
// commits the bot row AND the ledger response together; a replay returns the
// stored response without a second row (spec 5/E § Idempotency step 2).
func TestAppendAndFinishAtomic(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{pid: "pid-1"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	_ = db.PutTurnContext("t", "demo", "", "slack:T/C/U", "u1")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec.Code != 200 {
		t.Fatalf("reply status=%d", rec.Code)
	}
	// the bot row AND the ledger response committed together.
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("bot row not appended")
	}
	var ledgerStatus int
	var ledgerResp string
	db.SQL().QueryRow("SELECT status, response FROM idempotency_keys WHERE endpoint='POST /v1/turns/reply' AND key='k1'").
		Scan(&ledgerStatus, &ledgerResp)
	if ledgerStatus != 200 || ledgerResp == "" {
		t.Fatalf("ledger not finished atomically: status=%d resp=%q", ledgerStatus, ledgerResp)
	}

	// replay returns the stored response, no second row, no second delivery.
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/t/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec2.Code != 200 || rec2.Body.String() != ledgerResp {
		t.Fatalf("replay status=%d body=%q want stored %q", rec2.Code, rec2.Body.String(), ledgerResp)
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("replay appended a second row")
	}
	if len(dl.sends) != 1 {
		t.Fatalf("replay re-delivered (sends=%d)", len(dl.sends))
	}
}

// runnerFn adapts a func to the Runner interface for loop tests.
type runnerFn func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error)

func (f runnerFn) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	if f == nil {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	}
	return f(ctx, req)
}

func statusOf(db *DB, id string) string {
	var st string
	db.SQL().QueryRow("SELECT status FROM messages WHERE id=?", id).Scan(&st)
	return st
}

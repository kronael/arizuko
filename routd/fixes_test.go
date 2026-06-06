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
	voices   []sentVoice
	reacts   int
	posts    int
	dislikes int
	failSend bool
	pid      string
	// history is the canned HistoryResponse JSON the FetchHistory fake returns;
	// historyErr forces the adapter-failed branch (→ cache fallback). Empty
	// history + nil err → an empty-but-valid response.
	history    []byte
	historyErr error
}

type sentMsg struct {
	jid, text, replyTo, threadID, idem string
}

type sentVoice struct {
	jid, audioPath, caption, threadID string
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
func (d *recDeliverer) React(_, _, _ string) error      { d.reacts++; return nil }
func (d *recDeliverer) Edit(_, _, _ string) error       { return nil }
func (d *recDeliverer) Delete(_, _ string) error        { return nil }
func (d *recDeliverer) Pin(_, _ string) error           { return nil }
func (d *recDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (d *recDeliverer) Document(_, _, _, _, _, _ string) (string, error) {
	return d.pid, nil
}
func (d *recDeliverer) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	d.voices = append(d.voices, sentVoice{jid, audioPath, caption, threadID})
	return d.pid, nil
}
func (d *recDeliverer) Post(_, _ string, _ []string) (string, error)       { d.posts++; return d.pid, nil }
func (d *recDeliverer) Forward(_, _, _ string) (string, error)             { return d.pid, nil }
func (d *recDeliverer) Quote(_, _, _ string) (string, error)               { return d.pid, nil }
func (d *recDeliverer) Repost(_, _ string) (string, error)                 { return d.pid, nil }
func (d *recDeliverer) Dislike(_, _ string) error                          { d.dislikes++; return nil }
func (d *recDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error { return nil }
func (d *recDeliverer) SetName(_, _ string) error                          { return nil }
func (d *recDeliverer) RoundDone(_, _, _, _ string) error                  { return nil }
func (d *recDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return d.history, d.historyErr
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
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
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
	db.PutTurnContext("old", "demo", "", "slack:T/C/U", "u1", "")
	db.SQL().Exec("UPDATE turn_context SET started_at=? WHERE turn_id='old'",
		time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano))
	// a turn started just now, still running.
	db.PutTurnContext("new", "demo", "", "slack:T/C/U2", "u1", "")

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
	db.PutTurnContext("t", "demo", "", "slack:T/C/U", "u1", "")

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
	db.PutTurnContext("t", "demo", "", "slack:T/C/U", "u1", "")
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

// TestRoutesReplaceScopedToSubtree: a folder-scoped caller replacing its own
// routes must NOT wipe a sibling folder's routes. Before the fix
// handleRoutesReplace ran a global DELETE FROM routes (spec 5/E § route
// ownership): folder A's PUT erased folder B.
func TestRoutesReplaceScopedToSubtree(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:write:own_group"}, folder: "a"})
	// Seed folder B's route directly (a different owner put it there).
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "b"}); err != nil {
		t.Fatal(err)
	}
	// Folder A replaces ITS routes.
	rec := doJSON(t, h, "PUT", "/v1/routes", "",
		[]apiv1.Route{{Seq: 2, Match: "platform=tg", Target: "a"}})
	if rec.Code != 200 {
		t.Fatalf("scoped replace = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	routes, err := db.Routes()
	if err != nil {
		t.Fatal(err)
	}
	var sawA, sawB bool
	for _, r := range routes {
		switch r.Target {
		case "a":
			sawA = true
		case "b":
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("scoped replace by folder a wiped folder b's route")
	}
	if !sawA {
		t.Fatal("scoped replace did not insert folder a's route")
	}
}

// TestRoutesListScopedToSubtree: a folder-scoped read token sees only its own
// subtree's routes, not the whole table (info-leak fix; matches gated /me).
func TestRoutesListScopedToSubtree(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:read:own_group"}, folder: "a"})
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "m", Target: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 2, Match: "m", Target: "a/sub"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 3, Match: "m", Target: "b"}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "GET", "/v1/routes", "", nil)
	if rec.Code != 200 {
		t.Fatalf("list = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var out []apiv1.Route
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		if r.Target == "b" {
			t.Fatalf("scoped list leaked sibling folder route: %q", r.Target)
		}
	}
	if len(out) != 2 {
		t.Fatalf("scoped list returned %d routes want 2 (a, a/sub)", len(out))
	}
}

// TestScanMessagesAbortsOnRowError: a malformed row (a NULL in a column scanned
// into a non-nullable Go type) must ABORT the scan with an error, not silently
// skip the row and advance the cursor past it (silent message loss).
func TestScanMessagesAbortsOnRowError(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// sender_name is nullable in the schema but scanned into a plain string;
	// a NULL there fails rows.Scan.
	if _, err := db.SQL().Exec(`INSERT INTO messages
		(id, chat_jid, sender, sender_name, content, timestamp, verb, status)
		VALUES ('m1','slack:T/C/U','u',NULL,'hi','2026-01-01T00:00:00Z','message','sent')`); err != nil {
		t.Fatal(err)
	}
	_, hi, err := db.NewMessages("")
	if err == nil {
		t.Fatal("scan over a malformed row returned nil error (silent skip)")
	}
	if hi != "" {
		t.Fatalf("cursor advanced to %q past the unscanned row", hi)
	}
}

// TestRouteGetStoreErrorReturns500: handleRouteGet must surface a Routes()
// store error as 500, not a spurious 404. Before the fix the error was
// discarded (`routes, _ :=`) so a transient DB fault looked like "route not
// found".
func TestRouteGetStoreErrorReturns500(t *testing.T) {
	db, h := authSrv(t, nil) // open mode; isolates the store path
	if _, err := db.SQL().Exec("DROP TABLE routes"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "GET", "/v1/routes/1", "", nil)
	if rec.Code != 500 {
		t.Fatalf("route get with failing store = %d want 500 body=%s", rec.Code, rec.Body.String())
	}
	var e apiv1.Err
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error != "store_error" {
		t.Fatalf("error=%q want store_error (body=%s)", e.Error, rec.Body.String())
	}
}

// TestRouteDeleteStoreErrorReturns500: a folder-scoped caller's delete must
// surface a Routes() store error as 500, NOT fall through to an unconditional
// DeleteRoute. Before the fix the scope-check's `routes, _ :=` swallowed the
// error → the ownership loop found nothing → a scoped token could delete any
// route. We assert 500 (the scope-check can no longer be silently bypassed).
func TestRouteDeleteStoreErrorReturns500(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:write:own_group"}, folder: "a"})
	if _, err := db.SQL().Exec("DROP TABLE routes"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "DELETE", "/v1/routes/1", "", nil)
	if rec.Code != 500 {
		t.Fatalf("scoped route delete with failing store = %d want 500 body=%s", rec.Code, rec.Body.String())
	}
	var e apiv1.Err
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error != "store_error" {
		t.Fatalf("error=%q want store_error (body=%s)", e.Error, rec.Body.String())
	}
}

// TestDocumentStampsPlatformID: the /document REST twin must persist the
// platform id the Deliverer returns onto the stored bot row AND echo it in the
// SendResult. Before the fix it discarded the returned pid (`if _, err := ...`)
// so the document row landed with empty platform_id, breaking later
// edit/delete/pin targeting that message. Mirrors how /reply + /send already
// capture pid (turns.go appendAndDeliver).
func TestDocumentStampsPlatformID(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &fakeDeliverer{platformID: "doc-pid-99"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/document", "doc-1",
		apiv1.DocumentRequest{JID: "slack:T/C/U", Path: "/srv/x.pdf", Name: "x.pdf"})
	if rec.Code != 200 {
		t.Fatalf("document status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out apiv1.SendResult
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.PlatformID != "doc-pid-99" {
		t.Fatalf("SendResult.PlatformID=%q want doc-pid-99", out.PlatformID)
	}
	if out.Status != core.MessageStatusSent {
		t.Fatalf("SendResult.Status=%q want sent", out.Status)
	}
	// The stored bot row must carry the platform id for later mutations to resolve.
	var pid string
	db.SQL().QueryRow(
		"SELECT COALESCE(platform_id,'') FROM messages WHERE chat_jid=? AND is_bot_message=1",
		"slack:T/C/U").Scan(&pid)
	if pid != "doc-pid-99" {
		t.Fatalf("stored document row platform_id=%q want doc-pid-99", pid)
	}
}

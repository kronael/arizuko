package routd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// recDeliverer records every egress call and optionally fails Send.
type recDeliverer struct {
	// mu guards sends. The hold fixture runs its gate on its own goroutine —
	// that is what a hold IS — so the notice lands from there while the test
	// reads it, and the read used to find an empty slice (index out of range on
	// a loaded host, and a data race under -race).
	mu      sync.Mutex
	sends   []sentMsg
	edits   []sentEdit
	editErr error
	// noEdit makes the channel not advertise `edit`: a progress update is
	// dropped, never sent as a fresh message.
	noEdit   bool
	docs     []sentMsg
	voices   []sentVoice
	reacts   int
	posts    int
	dislikes int
	failSend bool
	// failIdem fails ONLY the send whose idempotency key (the message id)
	// matches, so a test can put one dead row among healthy ones.
	failIdem string
	// permIdem fails the send whose idempotency key matches with a
	// chanlib.ErrPermanent error — the platform refuses the message for good.
	permIdem string
	pid      string
	// viaChannels records the explicit adapter pin of every SendVia call
	// (spec 5/34-channel-protocol outbound resolution step 1).
	viaChannels []string
	// roundDones records every RoundDone call's chat JID (the SSE subscriber
	// key, verbatim), to assert publishRoundDone passes it underived.
	roundDones []string
	// history is the canned HistoryResponse JSON the FetchHistory fake returns;
	// historyErr forces the adapter-failed branch (→ cache fallback). Empty
	// history + nil err → an empty-but-valid response.
	history    []byte
	historyErr error
	// typings records every Typing call, in order.
	typings []typingEvent
}

// sentMsgs is the safe read of what landed: the gate appends from another
// goroutine, so a test that touched the slice directly would race it.
func (d *recDeliverer) sentMsgs() []sentMsg {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]sentMsg(nil), d.sends...)
}

type sentMsg struct {
	jid, text, replyTo, threadID, threadRoot, idem string
	options                                        []chanlib.Option
}

type sentVoice struct {
	jid, audioPath, caption, threadID string
}

type sentEdit struct {
	jid, platformID, content string
	threadID                 string
}

func (d *recDeliverer) Send(jid, text, replyTo, threadID, threadRoot, idem string) (string, error) {
	d.mu.Lock()
	d.sends = append(d.sends, sentMsg{jid, text, replyTo, threadID, threadRoot, idem, nil})
	d.mu.Unlock()
	if d.permIdem != "" && idem == d.permIdem {
		return "", fmt.Errorf("%w: status 400", chanlib.ErrPermanent)
	}
	if d.failSend || (d.failIdem != "" && idem == d.failIdem) {
		return "", errSend
	}
	if d.pid == "" {
		return "pid-" + idem, nil
	}
	return d.pid, nil
}

// SendOptions records the pickable answers alongside the text so a test can
// assert what a reader is actually offered.
func (d *recDeliverer) SendOptions(jid, text string, options []chanlib.Option, threadID, idem string) (string, error) {
	id, err := d.Send(jid, text, "", threadID, "", idem)
	// Send appends under the lock and releases it, so stamping the options back
	// onto that row is a second write and needs the lock again. The hold notice
	// is delivered through THIS method, and it is the one a waiting test reads.
	// The index panics on an empty slice, and a panic between Lock and Unlock
	// holds the mutex for good — every later Send then blocks and the package
	// dies on the test timeout instead of failing this one test.
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sends[len(d.sends)-1].options = options
	return id, err
}

// SendVia records the pin alongside the send so a test can assert which adapter
// an explicit `channel` selected.
func (d *recDeliverer) SendVia(channel, jid, text string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.viaChannels = append(d.viaChannels, channel)
	return d.Send(jid, text, "", "", "", "")
}
func (d *recDeliverer) React(_, _, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reacts++
	return nil
}
func (d *recDeliverer) Edit(jid, platformID, content, threadID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edits = append(d.edits, sentEdit{jid, platformID, content, threadID})
	return d.editErr
}
func (d *recDeliverer) CanEdit(_ string) bool           { return !d.noEdit }
func (d *recDeliverer) Delete(_, _ string) error        { return nil }
func (d *recDeliverer) Pin(_, _ string) error           { return nil }
func (d *recDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (d *recDeliverer) Typing(jid string, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typings = append(d.typings, typingEvent{jid, on})
	return nil
}
func (d *recDeliverer) Document(jid, _, _, _, replyTo, threadID, idem string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.docs = append(d.docs, sentMsg{jid: jid, replyTo: replyTo, threadID: threadID, idem: idem})
	return d.pid, nil
}
func (d *recDeliverer) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.voices = append(d.voices, sentVoice{jid, audioPath, caption, threadID})
	return d.pid, nil
}
func (d *recDeliverer) Post(_, _ string, _ []string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.posts++
	return d.pid, nil
}
func (d *recDeliverer) Forward(_, _, _ string) (string, error) { return d.pid, nil }
func (d *recDeliverer) Quote(_, _, _ string) (string, error)   { return d.pid, nil }
func (d *recDeliverer) Repost(_, _ string) (string, error)     { return d.pid, nil }
func (d *recDeliverer) Dislike(_, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dislikes++
	return nil
}
func (d *recDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error { return nil }
func (d *recDeliverer) SetName(_, _ string) error                          { return nil }
func (d *recDeliverer) RoundDone(chatJID, _, _, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roundDones = append(d.roundDones, chatJID)
	return nil
}
func (d *recDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return d.history, d.historyErr
}

var errSend = errSendT("send failed")

type errSendT string

func (e errSendT) Error() string { return string(e) }

// TestSubmitStatusDeliversInterimWithoutEndingTurn is the mid-turn streaming
// fix: submit_status delivers the agent's <status> text immediately as a "⏳ ..."
// notice and leaves the turn open — the turn ends only on submit_turn. So the
// user sees progress during a long turn instead of nothing until the end.
func TestSubmitStatusDeliversInterimWithoutEndingTurn(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	if err := srv.mcpSubmitStatus("t1", "reading 12 files"); err != nil {
		t.Fatalf("submit_status: %v", err)
	}
	if len(dl.sends) != 1 || dl.sends[0].text != "⏳ reading 12 files" {
		t.Fatalf("interim notice not delivered, got %+v", dl.sends)
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("interim status row not persisted")
	}
	// The turn must remain open: no submit_turn yet, so state is not done.
	if tc, _ := db.GetTurnContext("t1"); tc.State == "done" || tc.RunReturned {
		t.Fatalf("submit_status ended the turn: state=%q run_returned=%v", tc.State, tc.RunReturned)
	}

	// After the run returns the callback closes — a trailing status is a no-op.
	_ = db.SetRunReturned("t1")
	if err := srv.mcpSubmitStatus("t1", "too late"); err != nil {
		t.Fatalf("submit_status post-close: %v", err)
	}
	if len(dl.sends) != 1 {
		t.Fatalf("status delivered after run returned: %+v", dl.sends)
	}
}

// TestSubmitStatusEditsLiveMessageInPlace is spec 5/24: the first status of a
// turn sends one "⏳ …" message; each later status EDITS that same message
// instead of appending a new notice — a live checklist that updates in place.
func TestSubmitStatusEditsLiveMessageInPlace(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	for _, s := range []string{"step 1", "step 2", "step 3"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatalf("submit_status %q: %v", s, err)
		}
	}
	if len(dl.sends) != 1 {
		t.Fatalf("later statuses appended new messages, want one send + edits: sends=%+v", dl.sends)
	}
	if len(dl.edits) != 2 || dl.edits[1].content != "⏳ step 3" {
		t.Fatalf("live edits wrong: %+v", dl.edits)
	}
	if dl.edits[0].platformID != "pid-"+dl.sends[0].idem {
		t.Fatalf("edit did not target the first status message: edit=%q send=pid-%q", dl.edits[0].platformID, dl.sends[0].idem)
	}
}

// TestSubmitStatusHardEditErrorSurfaces: a real edit failure (401/500/timeout,
// not ErrUnsupported) is returned, NOT silently re-posted as a duplicate.
func TestSubmitStatusHardEditErrorSurfaces(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{editErr: errSend}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := srv.mcpSubmitStatus("t1", "b"); err == nil {
		t.Fatal("hard edit error should surface, not fall through to a duplicate send")
	}
	if len(dl.sends) != 1 {
		t.Fatalf("hard edit error re-posted a duplicate: sends=%+v", dl.sends)
	}
}

// TestStatusRowDoesNotSuppressReply is the critical guard (spec 5/24): a verb=
// status ⏳ row must NOT count as the agent's reply, or recordTurnResult skips
// delivering the real answer (every multi-step turn since the auto-TodoWrite
// status would lose its prose). A genuine reply row still counts.
func TestStatusRowDoesNotSuppressReply(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	if db.TurnHasBotReply("slack:T/C/U", "t1") {
		t.Fatal("a ⏳ status row counted as the reply — the agent's answer would be swallowed")
	}
	// A genuine reply row DOES count.
	_ = db.PutOutbound(core.Message{
		ID: "r1", ChatJID: "slack:T/C/U", Sender: "demo", Content: "the answer",
		BotMsg: true, FromMe: true, Verb: "reply", TurnID: "t1", Timestamp: time.Now()}, core.MessageStatusSent)
	if !db.TurnHasBotReply("slack:T/C/U", "t1") {
		t.Fatal("a real reply row must count as the bot reply")
	}
}

// TestDispatchTimedTriggerNoUserSecrets: a timed-isolated trigger must NOT
// ship user-scoped secrets (caller = "service:routd", not a real user sub).
// Regression guard: if dispatchRun ever resolved the timed sender as a user
// JID, alice's BYOA key would silently leak into every cron turn.
func TestDispatchTimedTriggerNoUserSecrets(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetSecretKeys([]byte("byoa-timed-key"))
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.SetSecret(store.ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "alice-key")

	var got map[string]string
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		got = req.Secrets
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=web", Target: "demo"}})
	_ = db.PutMessage(core.Message{
		ID: "t", ChatJID: "web:demo", Sender: "timed-isolated:compact-memories",
		Content: "compact now", Timestamp: time.Now().UTC(), Verb: "message",
	})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got["ANTHROPIC_API_KEY"] == "alice-key" {
		t.Error("timed trigger leaked alice's user-scoped BYOA key into RunRequest.Secrets")
	}
}

// TestDispatchShipsUserSecrets is the BYOA wire test: a user's user-scoped
// secret is resolved at dispatch (overriding the folder default) and set on
// RunRequest.Secrets so runed injects it as container env. The web sender IS the
// user's canonical sub, so caller==scope_id and the override resolves.
func TestDispatchShipsUserSecrets(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetSecretKeys([]byte("byoa-dispatch-key"))
	_ = db.PutGroup(core.Group{Folder: "demo"})
	// folder default (capability credential, allowed at folder scope) + user's BYOA override.
	_ = db.SetSecret(store.ScopeFolder, "demo", "GITHUB_TOKEN", "ghp_folder")
	_ = db.SetSecret(store.ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "alice-key")

	var got map[string]string
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		got = req.Secrets
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=web", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "m", ChatJID: "web:demo", Sender: "github:alice",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got["ANTHROPIC_API_KEY"] != "alice-key" {
		t.Errorf("RunRequest.Secrets[ANTHROPIC_API_KEY] = %q, want alice-key (user override shipped)", got["ANTHROPIC_API_KEY"])
	}
}

// TestRunErrorMarksAndNotifies is the outcome:error fix: a clean
// outcome:error advances past the batch, marks the chat errored, and sends a
// failure notice — not a silent success (spec 5/E-routd § outcome).
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
	// MaxTurnRetry=-1 disables retry so the first failure triggers immediate
	// error notice (testing the non-retry path).
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: -1})
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
	errored, eerr := db.ErroredChats("", true)
	if eerr != nil {
		t.Fatalf("errored chats: %v", eerr)
	}
	if len(errored) != 1 || errored[0].ChatJID != "slack:T/C/U" {
		t.Fatalf("chat not marked errored after outcome:error: %+v", errored)
	}
	// run_returned set so a late callback 409s.
	if tc, _ := db.GetTurnContext("e"); !tc.RunReturned {
		t.Fatal("run_returned not set after run-response")
	}
}

// The T2 orphan: a follow-up steered into a live run is marked done and returns
// long before that run dies, so the failure notice has to reach its chat too.
// routd steers by FOLDER, so that chat is not always the running turn's, and
// the steered user's message was accepted and then produced neither a reply nor
// an error.
func TestRunErrorNotifiesEveryChatTheRunOwned(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	dl := &recDeliverer{}
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		// Mid-run a second chat of the same folder is steered into this live
		// container, which records that turn against the running run.
		if _, perr := db.PutTurnContext("steered", "demo", "", "slack:T/B/U", "u2", ""); perr != nil {
			t.Errorf("seed steered turn: %v", perr)
		}
		if serr := db.SetTurnRunID("steered", "run-1"); serr != nil {
			t.Errorf("set steered run id: %v", serr)
		}
		return runedv1.RunOutcome{RunID: "run-1", Outcome: runedv1.OutcomeError, Error: "boom"}, nil
	})
	// MaxTurnRetry=-1 disables retry so the first failure notifies immediately.
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: -1})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "e", ChatJID: "slack:T/A/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, perr := loop.processGroupMessages("slack:T/A/U"); perr != nil {
		t.Fatalf("process: %v", perr)
	}

	text := map[string]string{}
	idem := map[string]string{}
	for _, s := range dl.sends {
		text[s.jid] = s.text
		idem[s.jid] = s.idem
	}
	for _, jid := range []string{"slack:T/A/U", "slack:T/B/U"} {
		if text[jid] != runFailureNotice {
			t.Errorf("chat %s got %q, want the failure notice: the run it was "+
				"waiting on died, so silence there is the bug", jid, text[jid])
		}
	}
	if idem["slack:T/A/U"] == idem["slack:T/B/U"] {
		t.Errorf("both notices carry idempotency key %q, so an adapter dedups "+
			"the fan-out into one delivery", idem["slack:T/A/U"])
	}
}

// TestTurnRetry_SchedulesRetryOnError verifies that a failed turn without a
// reply schedules a retry (spec 5/12).
func TestTurnRetry_SchedulesRetryOnError(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	dl := &recDeliverer{}
	var attempts int
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		attempts++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: "boom"}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: 2})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "r1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	// First attempt: schedules retry, no notice yet.
	_, _ = loop.processGroupMessages("slack:T/C/U")
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
	tc, _ := db.GetTurnContext("r1")
	if tc.RetryCount != 1 {
		t.Fatalf("expected retry_count=1, got %d", tc.RetryCount)
	}
	if tc.State != "pending_retry" {
		t.Fatalf("expected state=pending_retry, got %q", tc.State)
	}
	if len(dl.sends) != 0 {
		t.Fatalf("expected no notice on retry schedule, got %+v", dl.sends)
	}
	// THE RETRY IS THE RE-FEED, so the cursor must still sit behind the batch.
	// Advancing it leaves the next poll nothing to dispatch and the retry is a
	// no-op: every retry the fleet logged in a week died that way (BUGS L75a).
	if cur := db.GetAgentCursor("slack:T/C/U"); cur != "" {
		t.Fatalf("cursor advanced to %q past the batch the retry re-feeds", cur)
	}

	// Second attempt: the batch is held on the busy backoff, which a fresh
	// inbound message clears — expire it by hand and poll again.
	loop.busyWait.Delete("slack:T/C/U")
	_, _ = loop.processGroupMessages("slack:T/C/U")
	if attempts != 2 {
		t.Fatalf("the retry never re-dispatched: attempts = %d, want 2", attempts)
	}
	if tc, _ := db.GetTurnContext("r1"); tc.RetryCount != 2 {
		t.Fatalf("expected retry_count=2 after the second attempt, got %d", tc.RetryCount)
	}
}

// TestTurnRetry_TerminalNotRetried: a failure runed marked Terminal (bad
// image, failed docker start) is never retried, and the notice carries
// runed's error text so the user sees the real cause, not the generic
// failure text (spec 5/12, BUGS F73). Contrast
// TestTurnRetry_SchedulesRetryOnError: the same OutcomeError WITHOUT
// Terminal still schedules a retry — the zero value must stay retryable.
func TestTurnRetry_TerminalNotRetried(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	dl := &recDeliverer{}
	const cause = "Container exited with code 125: pull access denied for arizuko-ant"
	var attempts int
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		attempts++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: cause, Terminal: true}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: 2})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "tm1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	_, _ = loop.processGroupMessages("slack:T/C/U")
	if attempts != 1 {
		t.Fatalf("terminal failure spawned %d attempts, want 1 (retry burned on a misconfiguration)", attempts)
	}
	tc, _ := db.GetTurnContext("tm1")
	if tc.RetryCount != 0 {
		t.Fatalf("terminal failure incremented retry_count=%d, want 0", tc.RetryCount)
	}
	if tc.State != "done" {
		t.Fatalf("expected state=done after terminal failure, got %q", tc.State)
	}
	if len(dl.sends) != 1 {
		t.Fatalf("expected one failure notice, got %+v", dl.sends)
	}
	if got := dl.sends[0].text; got != terminalFailureNotice(cause) {
		t.Fatalf("notice=%q want terminalFailureNotice with the cause", got)
	}
	if !strings.Contains(dl.sends[0].text, "code 125") {
		t.Fatalf("notice does not name the real cause: %q", dl.sends[0].text)
	}
}

// TestTurnRetry_ExhaustsAndNotifies verifies that after maxTurnRetry failures
// without a reply, the user gets the exhausted notice (spec 5/12).
func TestTurnRetry_ExhaustsAndNotifies(t *testing.T) {
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
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: 2})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "r2", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	// Simulate retry_count already at max.
	_, _ = db.PutTurnContext("r2", "demo", "", "slack:T/C/U", "u1", "")
	db.IncrementRetryCount("r2")
	db.IncrementRetryCount("r2") // now retry_count=2, at max

	_, _ = loop.processGroupMessages("slack:T/C/U")
	if len(dl.sends) != 1 || dl.sends[0].text != retryExhaustedNotice(loop.maxTurnRetry) {
		t.Fatalf("expected retry exhausted notice, got %+v", dl.sends)
	}
	tc, _ := db.GetTurnContext("r2")
	if tc.State != "done" {
		t.Fatalf("expected state=done after exhaustion, got %q", tc.State)
	}
}

// TestGhostGroup_RefusesUnregistered verifies a route to a folder with no group
// row is refused loud + notified, never dispatched (GB1 L4a). This is the
// krons/content class: routed but unregistered → config-less spawn → silence.
func TestGhostGroup_RefusesUnregistered(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No PutGroup: "ghost" is routed but unregistered.
	dl := &recDeliverer{}
	spawned := false
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		spawned = true
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "ghost"}})
	_ = db.PutMessage(core.Message{ID: "g1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	_, _ = loop.processGroupMessages("slack:T/C/U")

	if spawned {
		t.Fatal("ghost group must not spawn a container")
	}
	if len(dl.sends) != 1 || dl.sends[0].text != ghostGroupNotice {
		t.Fatalf("expected ghost-group notice, got %+v", dl.sends)
	}
}

// TestDeleteGroup_CascadesRoutes verifies deleting a group removes every route
// into its subtree — the bare target, the observe variant on "#", and a child
// folder's — while a SIBLING whose name merely starts with the same characters
// keeps its own.
func TestDeleteGroup_CascadesRoutes(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "atlas"})
	_ = db.PutGroup(core.Group{Folder: "atlaswhatever"})
	doSetRoutes(t, db, []core.Route{
		{Seq: 0, Match: "room=1", Target: "atlas"},
		{Seq: 1, Match: "room=2", Target: "atlas#observe"},
		{Seq: 2, Match: "room=3", Target: "atlas/support"},
		{Seq: 3, Match: "room=4", Target: "atlas/support#observe"},
		{Seq: 4, Match: "room=5", Target: "atlaswhatever"},
	})

	if _, _, err := db.DeleteGroup("atlas"); err != nil {
		t.Fatal(err)
	}
	if db.GroupExists("atlas") {
		t.Fatal("group not deleted")
	}
	routes, _ := db.Routes()
	if len(routes) != 1 || routes[0].Target != "atlaswhatever" {
		t.Fatalf("expected only the sibling route to survive, got %+v", routes)
	}
}

// TestIngressIdempotencyKey is the POST /v1/messages key fix: a stable id +
// X-Idempotency-Key is ambiguous (400); a key with no id mints
// <adapter>-<key> (spec 5/E-routd § POST /v1/messages key rules).
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
	if !messageExists(db, "slack:T/C/U", "adapter-kk") {
		t.Fatal("minted row not stored")
	}
}

// TestReactionTopicInheritance is the reaction topic-inheritance fix: a
// reaction/reply with no topic inherits the parent's topic so it routes to the
// parent's thread (spec 5/E-routd § Channel ingress).
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
	react, _ := db.MessageByID("slack:T/C/U", "react")
	topic = react.Topic
	if topic != "deploy" {
		t.Fatalf("reaction topic=%q want deploy (inherited from parent)", topic)
	}
}

// TestOutboundRetryRedispatchesAndFails is the outboundRetryLoop fix: a
// pending bot row older than 30s is re-dispatched (adapter dedups on the
// stable id). Age alone never fails a row — the retired 24h bound killed a
// 25h-old row that no sweep had tried, which lost a message the platform had
// never refused. The attempt cap is the only bound (spec 5/Q § Outbound is
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
	// The rows are appended oldest first, because an append takes the chat's
	// next position and a row handed an instant below the tail is re-stamped
	// onto it (store.appendInstant).
	// pending for 25h → still re-dispatched: age is not a bound.
	_ = db.PutOutbound(core.Message{ID: "ancient", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "old but owed", Timestamp: now.Add(-25 * time.Hour), BotMsg: true}, core.MessageStatusPending)
	// pending for 2 min → re-dispatched.
	_ = db.PutOutbound(core.Message{ID: "stale", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "retry me", Timestamp: now.Add(-2 * time.Minute), BotMsg: true}, core.MessageStatusPending)
	// fresh (5s) → left alone.
	_ = db.PutOutbound(core.Message{ID: "fresh", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "wait", Timestamp: now.Add(-5 * time.Second), BotMsg: true}, core.MessageStatusPending)

	loop.nextRetryAt = now.Add(-time.Second) // due now
	loop.maybeRetryOutbound(now)

	// PendingOutbound orders by timestamp ASC, so the 25h row comes first.
	if len(dl.sends) != 2 || dl.sends[0].idem != "ancient" || dl.sends[1].idem != "stale" {
		t.Fatalf("expected re-dispatch of 'ancient' then 'stale', got %+v", dl.sends)
	}
	if st := statusOf(t, db, "stale"); st != core.MessageStatusSent {
		t.Fatalf("stale status=%q want sent (re-delivered)", st)
	}
	if st := statusOf(t, db, "ancient"); st != core.MessageStatusSent {
		t.Fatalf("ancient status=%q want sent — age alone must not fail a row", st)
	}
	if st := statusOf(t, db, "fresh"); st != core.MessageStatusPending {
		t.Fatalf("fresh status=%q want still pending (<30s)", st)
	}
}

// TestOutboundRetryFailureKeepsRowPendingAndContinues locks the durable form of
// head-of-line freedom. The retired in-memory chanreg outbox claimed this
// property and never delivered it: only a re-registration drained the queue, and
// a re-registration also REPLACED the channel object that held it, so the drain
// always ran on an empty, freshly built queue. The `messages` row is the real
// carrier. A send that fails must leave its row `pending` for the next sweep,
// and must not stop the sweep from attempting the rows behind it.
func TestOutboundRetryFailureKeepsRowPendingAndContinues(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// PendingOutbound orders by timestamp ASC, so the older 'dead' row is the
	// head of the batch — exactly the position that used to block the rest.
	dl := &recDeliverer{failIdem: "dead"}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl})

	now := time.Now().UTC()
	_ = db.PutOutbound(core.Message{ID: "dead", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "adapter refuses", Timestamp: now.Add(-2 * time.Minute), BotMsg: true}, core.MessageStatusPending)
	_ = db.PutOutbound(core.Message{ID: "live", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "deliver me", Timestamp: now.Add(-1 * time.Minute), BotMsg: true}, core.MessageStatusPending)

	loop.nextRetryAt = now.Add(-time.Second) // due now
	loop.maybeRetryOutbound(now)

	if len(dl.sends) != 2 {
		t.Fatalf("sweep attempted %d rows, want 2 — the dead head blocked the batch: %+v", len(dl.sends), dl.sends)
	}
	if st := statusOf(t, db, "dead"); st != core.MessageStatusPending {
		t.Errorf("dead row status=%q want pending (a failed send must stay retriable)", st)
	}
	if st := statusOf(t, db, "live"); st != core.MessageStatusSent {
		t.Errorf("live row status=%q want sent", st)
	}

	// The next sweep retries the still-pending row — the durable queue survives
	// where the in-memory one was discarded.
	dl.failIdem = ""
	loop.nextRetryAt = now.Add(-time.Second)
	loop.maybeRetryOutbound(now)
	if st := statusOf(t, db, "dead"); st != core.MessageStatusSent {
		t.Errorf("dead row status=%q want sent after the adapter recovered", st)
	}
}

// TestSweepExpiredRunning is the expired-sweep fix: a stale state='running'
// turn older than the run timeout flips to the DISTINCT 'expired' (not
// 'done'), so it stops being re-fed by crash recovery yet does not trip the
// done-guard against a legitimate re-dispatch (spec 5/E-routd § turn lifecycle).
func TestSweepExpiredRunning(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A turn started 2h ago, still running. Only SeedItem can place a turn in
	// the past, because the live writer stamps the instant itself, and that is
	// the point: `started_at` is written once.
	if err := store.NewOver(db.Obj()).SeedItem(t.Context(), store.TurnContexts,
		[]string{"old"}, map[string]string{
			"folder": "demo", "chat_jid": "slack:T/C/U", "trigger_sender": "u1",
			"state": "running", "started_at": core.TS(time.Now().Add(-2 * time.Hour)),
		}); err != nil {
		t.Fatalf("seed the stale turn: %v", err)
	}
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

// TestRoundDoneKeysOnChatJID guards the SSE-key-mismatch bug-class (BUGS L30):
// publishRoundDone must pass the CHAT JID VERBATIM (the webd SSE subscriber's
// key) — not the routing-target folder, and not a prefix-stripped derivation.
// A turn whose ChatJID is "web:alice/submissions" gets routed to group "bob";
// the round_done notice must carry "web:alice/submissions" (where the form UI
// subscribes), never "bob" — else the web form hangs waiting for a notice that
// landed on the wrong key.
func TestRoundDoneKeysOnChatJID(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "bob"})
	// A routing rule maps the web submission JID to a DIFFERENT target group.
	doSetRoutes(t, db, []core.Route{{Match: "platform=web", Target: "bob"}})

	dl := &recDeliverer{}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl})
	loop.StopQueue()
	srv := NewServer(db, loop, dl, nil, 0, "")
	loop.BindServer(srv)

	// The turn's chat JID is the web submission folder; its routed target is "bob".
	db.PutTurnContext("t1", "bob", "", "web:alice/submissions", "u1", "")

	if _, err := srv.recordTurnResult("t1", apiv1.TurnResult{TurnID: "t1", SessionID: "sX", Status: "success"}); err != nil {
		t.Fatal(err)
	}

	if len(dl.roundDones) != 1 {
		t.Fatalf("RoundDone fired %d times, want 1: %+v", len(dl.roundDones), dl.roundDones)
	}
	if got := dl.roundDones[0]; got != "web:alice/submissions" {
		t.Fatalf("RoundDone keyed on %q, want web:alice/submissions (the chat JID verbatim, not target bob)", got)
	}
}

// A hook: chat JID has no "web:" prefix, so the old
// strings.TrimPrefix(chatJID, "web:") was a silent no-op that LOOKED right but
// proved the derivation had no owner (BUGS L30). The notice must carry the
// hook JID verbatim so webd's hub key matches its subscribers.
func TestRoundDoneHookJIDVerbatim(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "acme"})

	dl := &recDeliverer{}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl})
	loop.StopQueue()
	srv := NewServer(db, loop, dl, nil, 0, "")
	loop.BindServer(srv)

	db.PutTurnContext("t2", "acme", "", "hook:acme/stripe", "u1", "")

	if _, err := srv.recordTurnResult("t2", apiv1.TurnResult{TurnID: "t2", SessionID: "sY", Status: "success"}); err != nil {
		t.Fatal(err)
	}

	if len(dl.roundDones) != 1 {
		t.Fatalf("RoundDone fired %d times, want 1: %+v", len(dl.roundDones), dl.roundDones)
	}
	if got := dl.roundDones[0]; got != "hook:acme/stripe" {
		t.Fatalf("RoundDone keyed on %q, want hook:acme/stripe (the chat JID verbatim)", got)
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

// statusOf answers one row's delivery status, which is stored as the ABSENCE of
// a delivery item: a row in neither the pending nor the failed partition is sent.
//
// It reads through the two feeds rather than a column, because there is no
// column — and reading them is also what the retry sweep and the dead-letter
// surface do, so a test and production ask the same question.
func statusOf(t *testing.T, db *DB, id string) string {
	t.Helper()
	if _, dead := db.DeadLetterByID(id); dead {
		return core.MessageStatusFailed
	}
	pending, err := db.PendingOutbound(time.Now().Add(time.Hour), 500)
	if err != nil {
		t.Fatalf("read the pending outbound feed: %v", err)
	}
	for _, p := range pending {
		if p.ID == id {
			return core.MessageStatusPending
		}
	}
	return core.MessageStatusSent
}

// TestRoutesReplaceScopedToSubtree: a folder-scoped caller replacing its own
// routes must NOT wipe a sibling folder's routes. Before the fix
// handleRoutesReplace ran a global DELETE FROM routes (spec 5/E-routd § route
// ownership): folder A's PUT erased folder B.
func TestRoutesReplaceScopedToSubtree(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:write:own_group"}, folder: "a"})
	// Seed folder B's route directly (a different owner put it there).
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "b"}); err != nil {
		t.Fatal(err)
	}
	// Folder A replaces ITS routes.
	rec := doJSON(t, h, "PUT", "/v1/routes", "",
		map[string]any{"routes": []apiv1.Route{{Seq: 2, Match: "platform=tg", Target: "a"}}})
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

// TestUnsetTextFieldsReadAsEmpty is the successor to the NULL-tolerance test.
// A nullable column COALESCEd to ” was the poll loop's hidden breaker: one NULL
// sender_name or reply_to_id aborted the scan, the cursor stuck, and no turn ever
// ran again. There is no column and no NULL now — an unset field is an attribute
// the item does not carry — so what has to hold is that such a message is still
// FED, with those fields reading as empty rather than failing the read.
func TestUnsetTextFieldsReadAsEmpty(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const jid = "slack:T/C/U"
	// No Name and no ReplyToID: absent is the only null this model has.
	if err := db.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "u", Content: "hi",
		Timestamp: time.Now(), Verb: "message",
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.NewMessagesForChat(jid, "", 200)
	if err != nil {
		t.Fatalf("an unset field aborted the read (the poll-killer): %v", err)
	}
	if len(msgs) != 1 || msgs[0].Name != "" || msgs[0].ReplyToID != "" {
		t.Fatalf("got %d msgs (want 1 with an empty name and reply_to_id): %+v", len(msgs), msgs)
	}
	// The cursor the loop would record has to be a whole position, or the next
	// poll hands this instant back.
	cursor := store.MessageCursor(msgs[0])
	if err := db.SetAgentCursor(jid, cursor); err != nil {
		t.Fatalf("the row produced no cursor the loop could record: %v", err)
	}
	again, err := db.NewMessagesForChat(jid, cursor, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("the consumed row was fed again: %+v", again)
	}
}

// TestInboundSourcePersistedForReplyRouting: an inbound carrying source (the
// adapter's CHANNEL_NAME) must persist it so LatestSource resolves the reply
// back to the ORIGINATING account. Regression (live multi-bot outage): the split
// dropped source in buildMessageRow → LatestSource returned "" → resolve fell to
// ForJID → replies to the rhias bot went out via the main bot. The pre-existing
// deliver_test MOCKED lookupSource, so it never caught that production never
// wrote source — this exercises the real persist→read chain.
func TestInboundSourcePersistedForReplyRouting(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := buildMessageRow(apiv1.Message{
		ID: "in1", ChatJID: "telegram:user/42", Sender: "telegram:user/42",
		Content: "hi", Source: "telegram-rhias",
	}, time.Now(), "message")
	if row.Source != "telegram-rhias" {
		t.Fatalf("buildMessageRow dropped source: %q", row.Source)
	}
	if err := db.PutMessage(row); err != nil {
		t.Fatal(err)
	}
	if got := db.LatestSource("telegram:user/42"); got != "telegram-rhias" {
		t.Fatalf("LatestSource = %q, want telegram-rhias (reply would route to the wrong bot)", got)
	}
}

// TestJidScheme: the per-surface output-style selector. A bare folder / operator
// / timed / auto-migrate JID (no scheme) must map to "" (default style), NOT the
// whole folder path — the SplitN bug the adversarial parity review caught
// (ChatJID="corp/eng" → bogus outputStyle="corp/eng").
func TestJidScheme(t *testing.T) {
	cases := map[string]string{
		"telegram:user/1112": "telegram",
		"slack:T/C/U":        "slack",
		"discord:dm/9":       "discord",
		"web:krons":          "web",
		"corp/eng":           "", // bare folder (auto-migrate/proactive path) → default
		"krons":              "", // operator
		"":                   "",
	}
	for jid, want := range cases {
		if got := jidScheme(jid); got != want {
			t.Errorf("jidScheme(%q) = %q, want %q", jid, got, want)
		}
	}
}

// TestWhapdFlatAttachmentFolded: whapd sends media via the flat
// attachment/attachment_mime/attachment_name fields (no array). buildMessageRow
// must fold them into a valid-JSON attachments array (gated api.go:271) so
// enrich.go can unmarshal + download it. Regression: the split stored the raw
// base64 → unmarshal fail → 100% of WhatsApp media silently dropped.
func TestWhapdFlatAttachmentFolded(t *testing.T) {
	row := buildMessageRow(apiv1.Message{
		ID: "w1", ChatJID: "whatsapp:user/5", Sender: "whatsapp:user/5",
		Attachment: "BASE64DATA", AttachmentMime: "image/jpeg", AttachmentName: "photo.jpg",
	}, time.Now(), "message")
	var atts []apiv1.Attachment
	if err := json.Unmarshal([]byte(row.Attachments), &atts); err != nil {
		t.Fatalf("attachments column is not valid JSON (media dropped): %v — got %q", err, row.Attachments)
	}
	if len(atts) != 1 || atts[0].Data != "BASE64DATA" || atts[0].Mime != "image/jpeg" || atts[0].Filename != "photo.jpg" {
		t.Fatalf("flat fields not folded: %+v", atts)
	}
}

// L66: a deleted group used to strand its 5 seeded `<folder>-mem-N` tasks.
// They stayed `active` and timed kept firing them at a folder that no longer
// existed — sloth ran Coach-mem-0..4 nightly for months, none of them ever
// executing. A child folder's tasks are its own and must survive, exactly as
// its routes do.
func TestDeleteGroup_CascadesTasks(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "atlas"})
	_ = db.PutGroup(core.Group{Folder: "atlas/support"})
	_ = db.PutGroup(core.Group{Folder: "atlaswhatever"})
	for _, tk := range []struct{ id, owner string }{
		{"atlas-mem-0", "atlas"},
		{"atlas-mem-1", "atlas"},
		{"atlas/support-mem-0", "atlas/support"},
		{"atlaswhatever-mem-0", "atlaswhatever"},
	} {
		next := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := db.store().CreateTask(core.Task{
			ID: tk.id, Owner: tk.owner, ChatJID: tk.owner, Prompt: "/compact-memories",
			Cron: "0 2 * * *", NextRun: &next, Status: "active",
			Created: next, ContextMode: "isolated",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := db.DeleteGroup("atlas"); err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, task := range db.Tasks("", true) {
		ids = append(ids, task.ID)
	}
	slices.Sort(ids)
	if len(ids) != 1 || ids[0] != "atlaswhatever-mem-0" {
		t.Fatalf("only the sibling folder's task may survive, got %v", ids)
	}
}

// TestEnsureTopicLineage_InheritsParentObservedCursor: a fork must continue the
// parent on BOTH axes. Stamping observed_cursor=now threw away the channel's
// ambient backlog the moment a thread was created — the copied transcript holds
// only turns that RAN in the parent, and a busy channel's chatter is
// is_observed=1 rows that never ran one, so a new Discord thread opened blind to
// everything said before it existed (BUGS L87).
func TestEnsureTopicLineage_InheritsParentObservedCursor(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	parentCursor := core.TS(time.Date(2026, 8, 23, 15, 10, 44, 0, time.UTC))
	if err := db.PutSession("main/trading", "", "root-uuid"); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceObservedCursor("main/trading", "", parentCursor); err != nil {
		t.Fatal(err)
	}

	inserted, err := db.EnsureTopicLineage("main/trading", "thread-1", "", "child-uuid")
	if err != nil || !inserted {
		t.Fatalf("EnsureTopicLineage: inserted=%v err=%v", inserted, err)
	}
	line, ok := db.TopicLineage("main/trading", "thread-1")
	if !ok {
		t.Fatal("no lineage row for the forked topic")
	}
	if line.ObservedCursor != parentCursor {
		t.Errorf("child observed_cursor = %q, want the parent's %q — a fork that stamps now starts blind",
			line.ObservedCursor, parentCursor)
	}

	// No parent row: empty cursor, so the child reads the whole trailing window
	// (already capped by observe_window_*), not nothing.
	if _, err := db.EnsureTopicLineage("main/other", "thread-2", "", "child2"); err != nil {
		t.Fatal(err)
	}
	line2, ok := db.TopicLineage("main/other", "thread-2")
	if !ok {
		t.Fatal("no lineage row for the cold-parent fork")
	}
	if line2.ObservedCursor != "" {
		t.Errorf("cold-parent child cursor = %q, want empty", line2.ObservedCursor)
	}
}

// The dual: a clean turn takes the errored flag away. Without it the flag is
// monotonic for the life of an instance and an operator reading "2 errored
// chats" learns that two chats once failed, not that anything is wrong now
// (BUGS L304). The CURSOR is not the clear — the failure path advances it too,
// which is what the test above pins.
func TestCleanRunClearsTheErroredFlag(t *testing.T) {
	db := memDB(t)
	const jid = "slack:T/C/U"
	if err := db.MarkChatErrored(jid); err != nil {
		t.Fatal(err)
	}
	if errored, err := db.ErroredChats("", true); err != nil || len(errored) != 1 {
		t.Fatalf("precondition: errored chats = %+v (err=%v), want one", errored, err)
	}

	dl := &recDeliverer{}
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl})
	loop.StopQueue()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "c", ChatJID: jid, Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}
	errored, err := db.ErroredChats("", true)
	if err != nil {
		t.Fatalf("errored chats: %v", err)
	}
	if len(errored) != 0 {
		t.Errorf("errored chats after a clean run = %+v, want none", errored)
	}
}

// awaitBudget is how long every wait in this package gives the thing it waits
// for. One second was the old budget and it held for a single test on an idle
// host; the whole package under -race is slow enough that the hold notice
// missed it, which failed a green suite about one run in five.
const awaitBudget = 15 * time.Second

// awaitCond polls cond until it reports true and fails the test with `what` if
// the budget runs out. It is the one wait in this package: a test that spins its
// own loop gets its own budget, and budgets drift.
func awaitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(awaitBudget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(what)
}

// waitSends blocks until the deliverer has recorded at least n sends. It is the
// happens-before a test needs before it reads `sends` directly: a gate on its
// own goroutine delivers after it records the held row, so waiting for the row
// is not waiting for the notice.
func (d *recDeliverer) waitSends(t *testing.T, n int) {
	t.Helper()
	awaitCond(t, fmt.Sprintf("the deliverer never recorded %d sends", n), func() bool {
		return len(d.sentMsgs()) >= n
	})
}

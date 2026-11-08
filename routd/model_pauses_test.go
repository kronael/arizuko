package routd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type pauseProvider struct {
	t            *testing.T
	mu           sync.Mutex
	srv          *Server
	runs         []runedv1.RunRequest
	reasons      []string
	blockAt      int
	started      chan struct{}
	release      chan struct{}
	stopErr      error
	stops        int
	busy         bool
	noSubmission bool
}

func (p *pauseProvider) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	p.mu.Lock()
	p.runs = append(p.runs, req)
	n := len(p.runs)
	reason := ""
	if n <= len(p.reasons) {
		reason = p.reasons[n-1]
	}
	p.mu.Unlock()
	if n == p.blockAt {
		release := p.release
		close(p.started)
		<-release
		return runedv1.RunOutcome{}, errors.New("interrupted run")
	}
	if p.busy {
		return runedv1.RunOutcome{Busy: true}, nil
	}
	if p.noSubmission {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, nil
	}
	result := ipc.TurnResult{TurnID: req.TurnID, Status: "success", SessionID: "preserved"}
	if reason == "" {
		p.srv.replyForTest(p.t, req.TurnID, req.ChatJID, "recovered")
	}
	if reason != "" {
		result.Status, result.Error = "error", "provider error envelope"
		result.ProviderFailure = &apiv1.ProviderFailure{Reason: reason}
	}
	if _, err := p.srv.mcpSubmitTurn(string(req.Folder), result); err != nil {
		return runedv1.RunOutcome{}, err
	}
	out := runedv1.RunOutcome{RunID: fmt.Sprintf("run-%d", n), Outcome: runedv1.OutcomeOK}
	if reason != "" {
		out.Outcome, out.Terminal = runedv1.OutcomeError, true
	}
	return out, nil
}

func (p *pauseProvider) StopFolder(context.Context, string) (runedv1.StopRunResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	if p.stopErr != nil {
		return runedv1.StopRunResponse{}, p.stopErr
	}
	if p.release != nil {
		close(p.release)
		p.release = nil
	}
	return runedv1.StopRunResponse{}, nil
}

func pauseFixture(t *testing.T, reasons ...string) (*DB, *Loop, *pauseProvider, *recDeliverer) {
	t.Helper()
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	provider := &pauseProvider{t: t, reasons: reasons}
	loop.runner, loop.deliver = provider, dl
	provider.srv = NewServer(db, loop, dl, nil, 0, "")
	for _, folder := range []string{"a", "b"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	return db, loop, provider, dl
}

func pauseInput(t *testing.T, db *DB, jid, id, sender, content string, at time.Time) {
	t.Helper()
	if err := db.PutMessage(core.Message{ChatJID: jid, ID: id, Sender: sender, Content: content, Timestamp: at, Verb: "message"}); err != nil {
		t.Fatal(err)
	}
}

func processPauseChat(t *testing.T, l *Loop, jid string) {
	t.Helper()
	if _, err := l.processGroupMessages(jid); err != nil {
		t.Fatal(err)
	}
}

func pauseChatInFolder(t *testing.T, db *DB, jid, folder string) {
	t.Helper()
	if err := db.store().InsertRouteToken("pause-"+jid, store.RouteToken{JID: jid, OwnerFolder: folder, TargetFolder: folder}); err != nil {
		t.Fatal(err)
	}
}

func TestModelPauseForgedResetHoldsOnlyReportingFolder(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	if _, err := db.store().OpenTurnContext("forged", "a", "", "web:a", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	tc, ok := db.GetTurnContext("forged")
	if !ok {
		t.Fatal("missing turn")
	}
	fns := p.srv.buildRoutdFns(turnMCP{folder: "a", turnID: "forged", attempt: tc.AttemptID})
	_, err := fns.SubmitTurn("a", ipc.TurnResult{TurnID: "forged", Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "rate_limit", ResetAt: "2099-01-01T00:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.store().OpenTurnContext("same-folder", "a", "", "web:a", "u", "", nil); !errors.Is(err, store.ErrModelPaused) {
		t.Fatalf("same folder admitted: %v", err)
	}
	pauseInput(t, db, "web:b", "independent", "v", "run", time.Now())
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 1 || p.runs[0].TurnID != "independent" {
		t.Fatalf("other folder did not run: %+v", p.runs)
	}
	if tc, ok := db.GetTurnContext("forged"); !ok || tc.State != store.TurnHeld {
		t.Fatalf("reporting folder not held: %+v %v", tc, ok)
	}
}

func TestModelPauseNormalTurns429Then200(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	pauseInput(t, db, "web:a", "a-old", "u", "saved first", now)
	pauseInput(t, db, "web:b", "b-later", "v", "saved second", now.Add(time.Second))
	_ = db.PutSession("a", "", "original-session")
	processPauseChat(t, l, "web:a")
	before, _ := db.GetTurnContext("a-old")
	if before.State != store.TurnHeld || before.RetryCount != 0 || db.TurnResultRecorded("a", "a-old") || db.GetAgentCursor("web:a") != "" {
		t.Fatalf("refusal consumed input: %+v", before)
	}
	pauseInput(t, db, "web:a", "a-new", "u", "saved third", now.Add(2*time.Second))
	pauseInput(t, db, "web:a", "command", "u", "/new", now.Add(3*time.Second))
	l.noteBusy("web:a", "command")
	for range 2 {
		processPauseChat(t, l, "web:b")
		processPauseChat(t, l, "web:a")
	}
	if len(p.runs) != 1 || db.SessionID("a", "") != "original-session" || db.GetAgentCursor("web:a") != "" {
		t.Fatalf("waiting admitted or consumed input: runs=%d", len(p.runs))
	}
	if _, ok := l.busyWait.Load("web:a"); ok {
		t.Fatal("held input kept the ordinary busy backoff")
	}
	modelPauseDue(t, db)
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 1 {
		t.Fatal("newer input probed")
	}
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 2 || p.runs[1].TurnID != "a-old" || !strings.Contains(p.runs[1].MessageBatch, "saved first") || strings.Contains(p.runs[1].MessageBatch, "saved third") {
		t.Fatalf("probe changed held span: %+v", p.runs)
	}
	if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause != nil {
		t.Fatalf("probe did not clear: %+v %v", pause, err)
	}
	processPauseChat(t, l, "web:b")
	processPauseChat(t, l, "web:a")
	if len(p.runs) < 4 || p.runs[2].TurnID != "b-later" || p.runs[3].TurnID != "a-new" {
		t.Fatalf("saved order changed: %+v", p.runs)
	}
}

func TestModelPauseRestartWaitingAndRunningProbe(t *testing.T) {
	db, l, p, dl := pauseFixture(t, "spend_limit")
	pauseInput(t, db, "web:a", "held", "u", "keep me", time.Now())
	processPauseChat(t, l, "web:a")
	before, _ := db.store().ReadModelPause(t.Context(), "a")
	restarted := NewLoop(db, p, LoopConfig{Deliver: dl})
	restarted.StopQueue()
	p.srv = NewServer(db, restarted, dl, nil, 0, "")
	restarted.recoverPending()
	processPauseChat(t, restarted, "web:a")
	after, _ := db.store().ReadModelPause(t.Context(), "a")
	if *before != *after || len(p.runs) != 1 {
		t.Fatal("waiting restart changed pause or started a guest")
	}
	modelPauseDue(t, db)
	p.blockAt, p.started, p.release = 2, make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := restarted.processGroupMessages("web:a"); done <- err }()
	<-p.started
	bound, _ := db.store().ReadModelPause(t.Context(), "a")
	if bound.NextAttemptAt != "" {
		t.Fatal("running probe has a due time")
	}
	again := NewLoop(db, p, LoopConfig{Deliver: dl})
	again.StopQueue()
	p.stopErr = errors.New("runed unreachable")
	again.recoverPending()
	processPauseChat(t, again, "web:a")
	if len(p.runs) != 2 {
		t.Fatal("second probe admitted while old run unresolved")
	}
	p.stopErr = nil
	if err := again.recoverModelProbe(false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted probe outcome: %v", err)
	}
	p.srv = NewServer(db, again, dl, nil, 0, "")
	processPauseChat(t, again, "web:a")
	if len(p.runs) != 3 || p.stops < 2 {
		t.Fatalf("recovery: runs=%d stops=%d", len(p.runs), p.stops)
	}
	if db.GetAgentCursor("web:a") == "" {
		t.Fatal("recovered probe left input stuck")
	}
}

func modelPauseDue(t *testing.T, db *DB) {
	t.Helper()
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{
		Op: objstore.Update, Entity: store.ModelPauses,
		Key:   objstore.Key{Partition: store.OwnerRoutd, Sort: "a"},
		Attrs: map[string]string{"next_attempt_at": core.TS(time.Now().Add(-time.Second))},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestModelPauseRestartAfterSubmissionResolvesTheBoundProbe(t *testing.T) {
	db, l, p, dl := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	modelPauseDue(t, db)
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	if _, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "",
		&store.TurnAdmission{ProbeEpisode: pause.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	p.srv.replyForTest(t, "held", "web:a", "recovered")
	if _, err := p.srv.mcpSubmitTurn("a", ipc.TurnResult{TurnID: "held", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	restarted := NewLoop(db, p, LoopConfig{Deliver: dl})
	restarted.StopQueue()
	if err := restarted.recoverModelProbe(true); err != nil {
		t.Fatal(err)
	}
	if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause != nil {
		t.Fatalf("completed probe stranded after restart: %+v %v", pause, err)
	}
	processPauseChat(t, restarted, "web:a")
	if len(p.runs) != 1 || p.stops != 1 || db.GetAgentCursor("web:a") == "" {
		t.Fatalf("completed probe replayed: runs=%d stops=%d", len(p.runs), p.stops)
	}
}

func TestModelPauseAlreadyAdmittedTurnDoesNotProbeOrClear(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	pauseInput(t, db, "web:b", "admitted", "v", "in flight", now.Add(-time.Second))
	msgs, err := db.NewMessagesForChat("web:b", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.store().OpenTurnContext("admitted", "a", "", "web:b", "v", "",
		&store.TurnAdmission{InputLast: store.MessageCursor(msgs[0])}); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, "web:a", "held", "u", "saved", now)
	processPauseChat(t, l, "web:a")
	modelPauseDue(t, db)
	oldest, err := l.oldestModelInput("a")
	if err != nil || oldest.ID != "held" {
		t.Fatalf("already admitted input selected: %+v %v", oldest, err)
	}
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 1 {
		t.Fatal("already-admitted input stole the probe")
	}
	p.srv.replyForTest(t, "admitted", "web:b", "finished")
	if _, err := p.srv.mcpSubmitTurn("a", ipc.TurnResult{TurnID: "admitted", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause == nil {
		t.Fatalf("ordinary success cleared pause: %+v %v", pause, err)
	}
	processPauseChat(t, l, "web:b")
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 2 || p.runs[1].TurnID != "held" {
		t.Fatalf("wrong probe: %+v", p.runs)
	}
}

func TestModelPauseManualClearAndLateCallback(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "keep", time.Now())
	processPauseChat(t, l, "web:a")
	pauseInput(t, db, "web:a", "later", "u", "later input", time.Now().Add(time.Second))
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	ws := &store.WriteSet{}
	if err := db.store().ClearModelPauseInto(ws, *pause, errors.New("changed")); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetTurnContext("held")
	fns := p.srv.buildRoutdFns(turnMCP{folder: "a", turnID: "held", attempt: old.AttemptID})
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 2 || p.runs[1].TurnID != "held" ||
		strings.Contains(p.runs[1].MessageBatch, "later input") || db.GetAgentCursor("web:a") == "" {
		t.Fatal("manual clear did not resume")
	}
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 3 || p.runs[2].TurnID != "later" {
		t.Fatal("manual clear changed held input order")
	}
	if _, err := fns.SubmitTurn("a", ipc.TurnResult{TurnID: "held", Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}}); err == nil {
		t.Fatal("old attempt callback was accepted")
	}
}

func TestModelPauseNoticeDurableBeyondAppendWindow(t *testing.T) {
	db, l, _, dl := pauseFixture(t, "rate_limit")
	dl.failSend = true
	pauseInput(t, db, "web:a", "held", "u", "keep", time.Now())
	processPauseChat(t, l, "web:a")
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	tc, _ := db.GetTurnContext("held")
	for i := range 260 {
		pauseInput(t, db, "web:a", fmt.Sprintf("extra-%d", i), "u", "extra", time.Now())
	}
	for range 3 {
		if err := l.noticeModelPause(pause, tc); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := db.MessagesSince("web:a", "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "model-pause-") {
			n++
		}
	}
	pending, err := db.store().PendingOutbound(t.Context(), time.Now().Add(time.Hour), 500)
	if err != nil || n != 1 || len(pending) != 1 {
		t.Fatalf("notices=%d pending=%d %v", n, len(pending), err)
	}
	dl.failSend = false
	l.nextRetryAt = time.Time{}
	l.maybeRetryOutbound(time.Now().Add(time.Hour))
	if pending, err := db.store().PendingOutbound(t.Context(), time.Now().Add(2*time.Hour), 500); err != nil || len(pending) != 0 {
		t.Fatalf("notice did not retry: %+v %v", pending, err)
	}
}

func TestModelPauseForwardedNoticeAndOldestTies(t *testing.T) {
	db, l, p, dl := pauseFixture(t, "spend_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	if err := db.PutMessage(core.Message{ID: "held", ChatJID: "web:a", Sender: "u", Content: "forwarded", Timestamp: now, ForwardedFrom: "slack:origin"}); err != nil {
		t.Fatal(err)
	}
	processPauseChat(t, l, "web:a")
	pauseInput(t, db, "web:b", "later", "v", "other backend", now.Add(time.Second))
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 1 {
		t.Fatal("other group admitted during instance pause")
	}
	for _, jid := range []string{"slack:origin", "web:b"} {
		msgs, err := db.MessagesSince(jid, "")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range msgs {
			if strings.HasPrefix(m.ID, "model-pause-") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("notice destination %s has %d rows", jid, n)
		}
		stopped := false
		for _, event := range dl.typings {
			if event.jid == jid && !event.on {
				stopped = true
			}
		}
		if !stopped {
			t.Fatalf("waiting chat %s still types", jid)
		}
	}
	a := core.Message{ID: "same", ChatJID: "web:a", Timestamp: now}
	b := core.Message{ID: "same", ChatJID: "web:b", Timestamp: now}
	if !modelInputBefore(a, b) || modelInputBefore(b, a) {
		t.Fatal("chat JID tie break failed")
	}
	b.ID = "earlier"
	if !modelInputBefore(b, a) {
		t.Fatal("message ID tie break failed")
	}
}

func TestModelPauseBusyProbeRequeuesWithoutRecovery(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	before, _ := db.store().ReadModelPause(t.Context(), "a")
	modelPauseDue(t, db)
	p.busy = true
	processPauseChat(t, l, "web:a")
	after, _ := db.store().ReadModelPause(t.Context(), "a")
	if after == nil || after.OpenedAt != before.OpenedAt || after.ProbeAttemptID != "" || after.NextAttemptAt == "" || after.Attempts != 1 || db.GetAgentCursor("web:a") != "" {
		t.Fatalf("busy probe falsely recovered: %+v", after)
	}
	if tc, _ := db.GetTurnContext("held"); tc.State != store.TurnHeld || tc.RetryCount != 0 {
		t.Fatalf("busy changed held input: %+v", tc)
	}
}

func TestModelPauseFailureBeforeGuestSubmissionDoesNotProveRecovery(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	before, _ := db.store().ReadModelPause(t.Context(), "a")
	modelPauseDue(t, db)
	p.noSubmission = true
	processPauseChat(t, l, "web:a")
	after, _ := db.store().ReadModelPause(t.Context(), "a")
	if after == nil || after.OpenedAt != before.OpenedAt || after.ProbeAttemptID != "" || after.NextAttemptAt == "" || db.GetAgentCursor("web:a") != "" {
		t.Fatalf("failed spawn cleared or consumed probe: %+v", after)
	}
}

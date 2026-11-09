package routd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/queue"
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
	kills        []string
	busy         bool
	noSubmission bool
	localOutcome runedv1.RunOutcome
}

func (p *pauseProvider) Kill(_ context.Context, runID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kills = append(p.kills, runID)
	return p.stopErr
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
		return p.localOutcome, nil
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

func TestModelPauseHeldBindingSurvivesRouteChange(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	jid := "telegram:group/held"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	if err := db.SetEngagement(jid, "", "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStickyTopic(jid, "original"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutMessage(core.Message{ChatJID: jid, ID: "held-binding", Sender: "sender",
		Content: "saved", Timestamp: time.Now(), Verb: "message", ForwardedFrom: "telegram:group/origin"}); err != nil {
		t.Fatal(err)
	}
	processPauseChat(t, l, jid)
	before, ok := db.GetTurnContext("held-binding")
	if !ok || before.State != store.TurnHeld || !before.Addressed || before.ReturnTo != "telegram:group/origin" {
		t.Fatalf("held turn: %+v", before)
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "b"}})
	if err := db.SetEngagement(jid, "", "a", -time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStickyTopic(jid, "changed"); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, jid, "later-binding", "sender", "later", time.Now().Add(time.Second))
	modelPauseDue(t, db)
	processPauseChat(t, l, jid)
	after, _ := db.GetTurnContext("held-binding")
	if len(p.runs) != 2 || p.runs[1].Folder != "a" || p.runs[1].TurnID != before.TurnID ||
		p.runs[1].Topic != before.Topic || p.runs[1].SessionID != p.runs[0].SessionID ||
		after.Trigger != before.Trigger || after.ReturnTo != before.ReturnTo || after.Addressed != before.Addressed ||
		strings.Contains(p.runs[1].MessageBatch, "later") {
		t.Fatalf("held binding changed: before=%+v runs=%+v", before, p.runs)
	}
}

func TestModelPauseDeleteRecreateRetiresHeldInput(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("later_pending_%t", pending), func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "rate_limit")
			jid := "telegram:group/deleted"
			doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
			now := time.Now()
			pauseInput(t, db, jid, "old", "sender", "old tenant input", now)
			processPauseChat(t, l, jid)
			before, ok := db.GetTurnContext("old")
			if !ok || before.State != store.TurnHeld {
				t.Fatalf("turn was not held: %+v", before)
			}
			if pending {
				pauseInput(t, db, jid, "new", "sender", "new tenant input", now.Add(time.Second))
			}
			if _, _, err := db.store().DeleteGroup("a"); err != nil {
				t.Fatal(err)
			}
			if err := db.PutGroup(core.Group{Folder: "a"}); err != nil {
				t.Fatal(err)
			}
			after, ok := db.GetTurnContext("old")
			if !ok || after.State != store.TurnDone || !after.RunReturned || db.GetAgentCursor(jid) != before.InputLast {
				t.Fatalf("held input survived deletion: turn=%+v cursor=%q", after, db.GetAgentCursor(jid))
			}
			if msg, ok := db.MessageByID(jid, "old"); !ok || msg.Content != "old tenant input" {
				t.Fatalf("message history lost: %+v", msg)
			}
			if !pending {
				processPauseChat(t, l, jid)
				if len(p.runs) != 1 {
					t.Fatalf("held input ran without a route: %+v", p.runs)
				}
				pauseInput(t, db, jid, "new", "sender", "new tenant input", now.Add(time.Second))
			}
			doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
			processPauseChat(t, l, jid)
			if len(p.runs) != 2 || p.runs[1].TurnID != "new" || strings.Contains(p.runs[1].MessageBatch, "old tenant input") || !strings.Contains(p.runs[1].MessageBatch, "new tenant input") {
				t.Fatalf("path reuse dispatched wrong input: %+v", p.runs)
			}
		})
	}
}

func TestModelPauseExpiredEngagementPollsHeldInput(t *testing.T) {
	db, setup, p, dl := pauseFixture(t, "rate_limit")
	jid := "telegram:group/expiry"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a?engaged"}})
	if err := db.SetEngagement(jid, "", "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, jid, "expiry-held", "sender", "saved", time.Now())
	processPauseChat(t, setup, jid)
	if err := db.SetEngagement(jid, "", "a", -time.Hour); err != nil {
		t.Fatal(err)
	}
	modelPauseDue(t, db)
	l := NewLoop(db, p, LoopConfig{Deliver: dl, MaxRuns: 1})
	p.srv = NewServer(db, l, dl, nil, 0, "")
	t.Cleanup(l.StopQueue)
	if got := l.folderForJid(jid); got != "a" {
		t.Fatalf("queue folder = %q", got)
	}
	l.pollOnce()
	for l.q.ActiveCount() != 0 {
		runtime.Gosched()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.runs) != 2 || p.runs[1].TurnID != "expiry-held" || p.runs[1].Folder != "a" {
		t.Fatalf("held input did not probe: %+v cursor=%q", p.runs, db.GetAgentCursor(jid))
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

func TestModelPauseRecoveryDoesNotStopActiveFolder(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "keep", time.Now())
	processPauseChat(t, l, "web:a")
	modelPauseDue(t, db)
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	if _, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "probe", ProbeEpisode: pause.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	pause, _ = db.store().ReadModelPause(t.Context(), "a")
	l.q = queue.New(1)
	l.q.SetFolderForJidFn(func(string) string { return "a" })
	started, release := make(chan struct{}), make(chan struct{})
	l.q.SetProcessMessagesFn(func(string) (bool, error) { close(started); <-release; return true, nil })
	l.q.EnqueueMessageCheck("web:a")
	<-started
	if err := l.recoverFolderModelProbe(pause, true); err != nil {
		t.Fatal(err)
	}
	close(release)
	l.StopQueue()
	if p.stops != 0 {
		t.Fatal("recovery stopped the active folder")
	}
}

func TestModelPauseRequeueKillsCapturedRun(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "keep", time.Now())
	processPauseChat(t, l, "web:a")
	modelPauseDue(t, db)
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	if _, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "probe", ProbeEpisode: pause.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	pause, _ = db.store().ReadModelPause(t.Context(), "a")
	tc, _ := db.GetTurnContext("held")
	tc.RunID = "captured"
	p.stopErr = errors.New("runed unreachable")
	l.requeueModelProbe(pause, tc, true)
	after, _ := db.store().ReadModelPause(t.Context(), "a")
	if after.ProbeAttemptID != "probe" || len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
		t.Fatalf("failed kill changed pause or target: pause=%+v kills=%v stops=%d", after, p.kills, p.stops)
	}
	p.stopErr = &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"}
	l.requeueModelProbe(pause, tc, true)
	after, _ = db.store().ReadModelPause(t.Context(), "a")
	if after.ProbeAttemptID != "" || len(p.kills) != 2 || p.kills[1] != "captured" || p.stops != 0 {
		t.Fatalf("requeue did not kill captured run: pause=%+v kills=%v stops=%d", after, p.kills, p.stops)
	}
	l.requeueModelProbe(pause, tc, true)
	if len(p.kills) != 2 || p.stops != 0 {
		t.Fatalf("stale requeue stopped a run: kills=%v stops=%d", p.kills, p.stops)
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
	for _, tc := range []struct {
		name    string
		outcome runedv1.RunOutcome
		retry   bool
	}{
		{"terminal", runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, false},
		{"exhausted", runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, true},
		{"silent", runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "rate_limit")
			l.maxTurnRetry = 3
			pauseChatInFolder(t, db, "web:b", "a")
			now := time.Now()
			pauseInput(t, db, "web:a", "held", "u", "saved", now)
			processPauseChat(t, l, "web:a")
			pauseInput(t, db, "web:b", "next", "u", "next", now.Add(time.Second))
			processPauseChat(t, l, "web:b")
			before, _ := db.store().ReadModelPause(t.Context(), "a")
			p.noSubmission, p.localOutcome = true, tc.outcome
			if tc.retry {
				for count := 1; count <= l.maxTurnRetry; count++ {
					modelPauseDue(t, db)
					processPauseChat(t, l, "web:a")
					turn, _ := db.GetTurnContext("held")
					if turn.State != store.TurnHeld || turn.RetryCount != count {
						t.Fatalf("retry %d not held: %+v", count, turn)
					}
				}
			}
			modelPauseDue(t, db)
			processPauseChat(t, l, "web:a")
			after, _ := db.store().ReadModelPause(t.Context(), "a")
			turn, _ := db.GetTurnContext("held")
			if after == nil || after.OpenedAt != before.OpenedAt || after.Attempts != before.Attempts || after.ProbeAttemptID != "" || after.NextAttemptAt == "" || turn.State != store.TurnDone {
				t.Fatalf("local outcome did not finish and rearm: pause=%+v turn=%+v", after, turn)
			}
			p.noSubmission = false
			modelPauseDue(t, db)
			processPauseChat(t, l, "web:b")
			if p.runs[len(p.runs)-1].TurnID != "next" {
				t.Fatalf("next input did not probe: %+v", p.runs)
			}
			if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause != nil {
				t.Fatalf("next probe did not recover: %+v %v", pause, err)
			}
		})
	}
}

func TestModelPauseMissingCapturedRunRecovery(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprintf("completed=%t", completed), func(t *testing.T) {
			db, l, p, dl := pauseFixture(t, "rate_limit")
			pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
			processPauseChat(t, l, "web:a")
			modelPauseDue(t, db)
			pause, _ := db.store().ReadModelPause(t.Context(), "a")
			if _, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "", &store.TurnAdmission{AttemptID: "probe", ProbeEpisode: pause.OpenedAt}); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnRunID("held", "captured"); err != nil {
				t.Fatal(err)
			}
			if completed {
				p.srv.replyForTest(t, "held", "web:a", "recovered")
				if _, err := p.srv.mcpSubmitTurn("a", ipc.TurnResult{TurnID: "held", Status: "success"}); err != nil {
					t.Fatal(err)
				}
			}
			p.stopErr = fmt.Errorf("kill: %w", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"})
			if err := db.SetRunReturned("held"); err != nil {
				t.Fatal(err)
			}
			pauseInput(t, db, "web:b", "independent", "v", "run", time.Now())
			poller := NewLoop(db, p, LoopConfig{Deliver: dl})
			defer poller.StopQueue()
			polled := make(chan string, 4)
			poller.q.SetProcessMessagesFn(func(jid string) (bool, error) {
				polled <- jid
				return false, nil
			})
			poller.pollOnce()
			timeout := time.After(5 * time.Second)
		checkedPoll:
			for {
				select {
				case jid := <-polled:
					if jid == "web:b" {
						break checkedPoll
					}
				case <-timeout:
					t.Fatal("independent input did not poll")
				}
			}
			after, err := db.store().ReadModelPause(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			if completed && after != nil {
				t.Fatalf("completed pause remains: %+v", after)
			}
			if !completed && (after == nil || after.ProbeAttemptID != "" || after.NextAttemptAt == "") {
				t.Fatalf("pause not rearmed: %+v", after)
			}
			if len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
				t.Fatalf("kills=%v stops=%d", p.kills, p.stops)
			}
		})
	}
}

func TestModelPauseStopCapturedRunErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"success", nil, false},
		{"missing", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"}, false},
		{"wrapped missing", fmt.Errorf("wrapped: %w", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"}), false},
		{"other 404", &runedv1.APIError{Status: http.StatusNotFound, Code: "other"}, true},
		{"wrong status", &runedv1.APIError{Status: http.StatusInternalServerError, Code: "unknown_run"}, true},
		{"auth", &runedv1.APIError{Status: http.StatusUnauthorized, Code: "unauthorized"}, true},
		{"transport", errors.New("connection refused"), true},
		{"kill failure", &runedv1.APIError{Status: http.StatusInternalServerError, Code: "kill_failed"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &pauseProvider{stopErr: tt.err}
			l := &Loop{runner: p}
			err := l.stopModelProbe(TurnContext{RunID: "captured", Folder: "a"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v want error=%t", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, tt.err) {
				t.Fatalf("error %v does not preserve %v", err, tt.err)
			}
			if len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
				t.Fatalf("kills=%v stops=%d", p.kills, p.stops)
			}
		})
	}
}

type deadlinePauseProvider struct{ pauseProvider }

func (p *deadlinePauseProvider) Kill(ctx context.Context, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(6 * time.Second):
		return errors.New("no deadline")
	}
}
func (p *deadlinePauseProvider) StopFolder(ctx context.Context, _ string) (runedv1.StopRunResponse, error) {
	select {
	case <-ctx.Done():
		return runedv1.StopRunResponse{}, ctx.Err()
	case <-time.After(6 * time.Second):
		return runedv1.StopRunResponse{}, errors.New("no deadline")
	}
}

func TestModelPauseStopDeadline(t *testing.T) {
	for _, runID := range []string{"captured", ""} {
		t.Run(fmt.Sprintf("runID=%s", runID), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := &Loop{runner: &deadlinePauseProvider{}}
				start := time.Now()
				err := l.stopModelProbe(TurnContext{RunID: runID, Folder: "a"})
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
					t.Fatalf("stop error=%v duration=%v", err, time.Since(start))
				}
			})
		})
	}
}

func TestModelPausePollContinuesAfterRecoveryError(t *testing.T) {
	db, l, p, dl := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	modelPauseDue(t, db)
	pause, err := db.store().ReadModelPause(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "", &store.TurnAdmission{AttemptID: "probe", ProbeEpisode: pause.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnRunID("held", "captured"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunReturned("held"); err != nil {
		t.Fatal(err)
	}
	p.stopErr = context.DeadlineExceeded
	pauseInput(t, db, "web:b", "independent", "v", "run", time.Now())
	poller := NewLoop(db, p, LoopConfig{Deliver: dl})
	defer poller.StopQueue()
	polled := make(chan string, 4)
	poller.q.SetProcessMessagesFn(func(jid string) (bool, error) { polled <- jid; return false, nil })
	poller.pollOnce()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case jid := <-polled:
			if jid == "web:b" {
				goto checked
			}
		case <-timeout:
			t.Fatal("independent input did not poll")
		}
	}
checked:
	after, err := db.store().ReadModelPause(t.Context(), "a")
	if err != nil || after == nil || after.ProbeTurnID != "held" || after.ProbeAttemptID != "probe" {
		t.Fatalf("binding changed: %+v %v", after, err)
	}
}

func TestModelPauseDeleteClosesRunningCallbacks(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(fmt.Sprintf("probe=%t", probe), func(t *testing.T) {
			db, l, p, _ := pauseFixture(t)
			jid := "telegram:group/deleted"
			doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
			pauseInput(t, db, jid, "old", "sender", "private input", time.Now())
			msgs, err := db.NewMessagesForChat(jid, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			admission := &store.TurnAdmission{AttemptID: "running", InputLast: store.MessageCursor(msgs[0])}
			if _, err := db.store().OpenTurnContext("old", "a", "", jid, "sender", "", admission); err != nil {
				t.Fatal(err)
			}
			if probe {
				if _, err := db.store().RecordProviderFailure(t.Context(), "old", "running", "outage", time.Time{}, time.Now()); err != nil {
					t.Fatal(err)
				}
				modelPauseDue(t, db)
				pause, err := db.store().ReadModelPause(t.Context(), "a")
				if err != nil {
					t.Fatal(err)
				}
				admission.AttemptID, admission.ProbeEpisode = "probe", pause.OpenedAt
				if _, err := db.store().OpenTurnContext("old", "a", "", jid, "sender", "", admission); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err = db.store().DeleteGroupStopping(t.Context(), "a", func(context.Context, string) error {
				_, callbackErr := p.srv.mcpSubmitTurnAttempt(ipc.TurnResult{TurnID: "old", Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}}, admission.AttemptID)
				if callbackErr == nil {
					t.Error("deleted attempt accepted callback")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.PutGroup(core.Group{Folder: "a"}); err != nil {
				t.Fatal(err)
			}
			turn, ok := db.GetTurnContext("old")
			if !ok || turn.State != store.TurnDone || !turn.RunReturned || db.GetAgentCursor(jid) != admission.InputLast {
				t.Errorf("deleted input remains: turn=%+v cursor=%q", turn, db.GetAgentCursor(jid))
			}
			routes, err := db.Routes()
			if err != nil || len(routes) != 0 {
				t.Errorf("routes after deletion: %+v %v", routes, err)
			}
			pause, err := db.store().ReadModelPause(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			if pause != nil {
				t.Errorf("callback restored pause: %+v", pause)
				modelPauseDue(t, db)
			}
			processPauseChat(t, l, jid)
			if len(p.runs) != 0 {
				t.Errorf("private input dispatched without route: %+v", p.runs)
			}
			pauseInput(t, db, jid, "new", "sender", "new input", time.Now().Add(time.Second))
			doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
			processPauseChat(t, l, jid)
			if len(p.runs) != 1 || p.runs[0].TurnID != "new" || strings.Contains(p.runs[0].MessageBatch, "private input") {
				t.Errorf("replacement received wrong input: %+v", p.runs)
			}
		})
	}
}

type deleteGroupAtAdmission struct {
	objstore.Store
	fired bool
}

func (s *deleteGroupAtAdmission) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if !s.fired && w.Entity == store.TurnContexts && w.Op == objstore.Put {
			s.fired = true
			_, err := s.Store.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: store.Groups, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "a"}}})
			if err != nil {
				return false, err
			}
			break
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestMissingGroupAtAdmissionLeavesInputUnanswered(t *testing.T) {
	db, loop, provider, deliver := pauseFixture(t)
	pauseInput(t, db, "web:a", "input", "u", "question", time.Now())
	gate := &deleteGroupAtAdmission{Store: db.obj}
	db.obj = gate
	_, err := loop.processGroupMessages("web:a")
	if !gate.fired || !errors.Is(err, store.ErrGroupMissing) {
		t.Fatalf("admission: fired=%v err=%v", gate.fired, err)
	}
	if db.GetAgentCursor("web:a") != "" || len(provider.runs) != 0 || len(deliver.sentMsgs()) != 0 {
		t.Fatalf("input consumed or answered: cursor=%q runs=%d notices=%v", db.GetAgentCursor("web:a"), len(provider.runs), deliver.sentMsgs())
	}
	if p, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || p != nil {
		t.Fatalf("pause: %+v err=%v", p, err)
	}
}

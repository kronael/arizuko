package routd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestTurnControlBusyProbePreservesBudget(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "outage")
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	before, _ := db.GetTurnContext("refused")
	prior, _, _ := db.store().ReadTurnControl(t.Context())
	p.busy = true
	providerDue(t, l, "refused")
	after, _ := db.GetTurnContext("refused")
	control, _, err := db.store().ReadTurnControl(t.Context())
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	if err != nil || after.State != store.TurnPendingRetry || after.RetryCount != before.RetryCount ||
		control.Phase != "open" || control.ProbeAttemptID != "" || after.NextAttemptAt.IsZero() ||
		control.Episode != prior.Episode || control.ProbeTurnID != before.TurnID || pause == nil ||
		len(p.kills) != 0 || p.stops != 0 {
		t.Fatalf("busy probe changed retry ownership: turn=%+v control=%+v err=%v", after, control, err)
	}
}

func TestTurnControlDeleteClosesProviderCallbacks(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	pauseInput(t, db, "web:a", "running", "u", "private input", time.Now())
	msg, _ := db.MessageByID("web:a", "running")
	ok, err := db.store().OpenTurnContext("running", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "attempt", InputLast: store.MessageCursor(msg)})
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := db.GetTurnContext("running")
	_, _, err = db.store().DeleteGroupStopping(t.Context(), "a", func(context.Context, string) error {
		_, err := p.srv.recordTurnResultAttempt(tc.TurnID, tc.AttemptID,
			apiv1.TurnResult{Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}})
		if err == nil {
			return errors.New("deleted attempt accepted callback")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutGroup(core.Group{Folder: "a"}); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetTurnContext(tc.TurnID)
	if after.State != store.TurnDone || !after.RunReturned || db.GetAgentCursor(tc.ChatJID) != tc.InputLast {
		t.Fatalf("retired input survived recreation: %+v", after)
	}
	if history, ok := db.MessageByID(tc.ChatJID, "running"); !ok || history.Content != "private input" {
		t.Fatalf("deletion lost message history: %+v", history)
	}
	pauseChatInFolder(t, db, "web:a", "a")
	pauseInput(t, db, "web:a", "replacement", "u", "replacement input", msg.Timestamp.Add(time.Second))
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 1 || p.runs[0].TurnID != "replacement" || strings.Contains(p.runs[0].MessageBatch, "private input") {
		t.Fatalf("recreation dispatched retired input: %+v", p.runs)
	}
}

func TestTurnControlExpiredEngagementPollsSavedInput(t *testing.T) {
	db, setup, p, dl := pauseFixture(t, "outage")
	jid := "telegram:group/expiry"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a?engaged"}})
	if err := db.SetEngagement(jid, "", "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, jid, "saved", "u", "saved", time.Now())
	processPauseChat(t, setup, jid)
	if err := db.SetEngagement(jid, "", "a", -time.Hour); err != nil {
		t.Fatal(err)
	}
	poller := NewLoop(db, p, LoopConfig{Deliver: dl, MaxRuns: 1})
	t.Cleanup(poller.StopQueue)
	p.srv = NewServer(db, poller, dl, nil, 0, "")
	modelPauseDue(t, poller)
	poller.pollOnce()
	for poller.q.ActiveCount() != 0 {
		runtime.Gosched()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.runs) != 2 || p.runs[1].TurnID != "saved" || p.runs[1].Folder != "a" {
		t.Fatalf("expired engagement lost saved input: %+v", p.runs)
	}
}

func TestTurnControlForwardedNoticeAndOldestTies(t *testing.T) {
	db, l, _, dl := pauseFixture(t, "spend_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	if err := db.PutMessage(core.Message{ID: "held", ChatJID: "web:a", Sender: "u",
		Content: "forwarded", Timestamp: now, ForwardedFrom: "slack:origin"}); err != nil {
		t.Fatal(err)
	}
	processPauseChat(t, l, "web:a")
	pauseInput(t, db, "web:b", "later", "v", "other backend", now.Add(time.Second))
	processPauseChat(t, l, "web:b")
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
		stopped := false
		for _, event := range dl.typings {
			if event.jid == jid && !event.on {
				stopped = true
			}
		}
		if n != 1 || !stopped {
			t.Fatalf("notice/typing destination %s: notices=%d stopped=%v", jid, n, stopped)
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

func TestTurnControlProviderReplaySessionAndAddressed(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	jid := "telegram:group/saved"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	if err := db.SetEngagement(jid, "", "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSession("a", "", "original-session"); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, jid, "refused", "u", "saved", time.Now())
	processPauseChat(t, l, jid)
	before, _ := db.GetTurnContext("refused")
	if !before.Addressed || p.runs[0].SessionID != "original-session" {
		t.Fatalf("initial binding: turn=%+v run=%+v", before, p.runs[0])
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "b"}})
	if err := db.SetEngagement(jid, "", "a", -time.Hour); err != nil {
		t.Fatal(err)
	}
	providerDue(t, l, "refused")
	after, _ := db.GetTurnContext("refused")
	if len(p.runs) != 2 || p.runs[1].SessionID != p.runs[0].SessionID || after.Addressed != before.Addressed {
		t.Fatalf("replay lost session or Addressed: turn=%+v runs=%+v", after, p.runs)
	}
}

func TestTurnControlRecoveryDoesNotStopOccupiedFolder(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	tc := providerRefusal(t, db, p)
	ok, err := l.q.WithIdleFolder(tc.Folder, func() error {
		if err := l.q.recover(l, true); err != nil {
			return err
		}
		after, _ := db.GetTurnContext(tc.TurnID)
		if len(p.kills) != 0 || p.stops != 0 || after.RunReturned || after.State != store.TurnRunning {
			t.Fatalf("recovery stopped an occupied folder: turn=%+v kills=%v stops=%d", after, p.kills, p.stops)
		}
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("reserve: %v %v", ok, err)
	}
	if err := l.q.recover(l, true); err != nil {
		t.Fatal(err)
	}
	if len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
		t.Fatalf("idle recovery missed the captured run: kills=%v stops=%d", p.kills, p.stops)
	}
}

func TestTurnControlMissingCapturedRunRecovery(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			db, l, p, dl := pauseFixture(t, "outage")
			pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
			processPauseChat(t, l, "web:a")
			saved, _ := db.GetTurnContext("refused")
			l.q.now = func() time.Time { return saved.NextAttemptAt }
			_, ok, err := l.q.admit(l, saved, &store.TurnAdmission{AttemptID: "probe"}, &saved)
			if err != nil || !ok {
				t.Fatalf("probe: %v %v", ok, err)
			}
			if err := db.SetTurnRunID("refused", "captured"); err != nil {
				t.Fatal(err)
			}
			if completed {
				p.srv.replyForTest(t, "refused", "web:a", "recovered")
				if _, err := p.srv.recordTurnResultAttempt("refused", "probe", apiv1.TurnResult{Status: "success"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.SetRunReturned("refused"); err != nil {
				t.Fatal(err)
			}
			p.stopErr = fmt.Errorf("kill: %w", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"})
			pauseInput(t, db, "web:b", "command", "u", "/ping", time.Now())
			poller := NewLoop(db, p, LoopConfig{Deliver: dl})
			t.Cleanup(poller.StopQueue)
			poller.q.now = l.q.now
			var polled atomic.Int32
			poller.q.SetProcessMessagesFn(func(jid string) (bool, error) {
				if jid == "web:b" {
					polled.Add(1)
				}
				return false, nil
			})
			poller.pollOnce()
			for poller.q.ActiveCount() != 0 {
				runtime.Gosched()
			}
			after, _ := db.GetTurnContext("refused")
			control, _, _ := db.store().ReadTurnControl(t.Context())
			want := store.TurnPendingRetry
			if completed {
				want = store.TurnDone
			}
			if after.State != want || control.Phase != "open" || polled.Load() == 0 ||
				len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
				t.Fatalf("missing-run recovery: turn=%+v control=%+v polled=%d kills=%v", after, control, polled.Load(), p.kills)
			}
		})
	}
}

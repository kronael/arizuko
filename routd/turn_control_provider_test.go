package routd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func providerDue(t *testing.T, l *Loop, id string) {
	t.Helper()
	tc, _ := l.db.GetTurnContext(id)
	l.q.now = func() time.Time { return tc.NextAttemptAt }
	if _, err := l.replay(id); err != nil {
		t.Fatal(err)
	}
}

func modelPauseDue(t *testing.T, l *Loop) {
	t.Helper()
	control, _, err := l.db.store().ReadTurnControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	l.q.now = func() time.Time { return control.NextProbeAt }
}

func TestTurnControlProviderInstancePause(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	now := time.Now().UTC()
	l.q.now = func() time.Time { return now }
	pauseInput(t, db, "web:a", "refused", "u", "saved", now)
	msg, _ := db.MessageByID("web:a", "refused")
	keys, _ := turnMessageKeys([]core.Message{msg})
	_, err := db.store().OpenTurnContext("refused", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "attempt", InputLast: store.MessageCursor(msg), InputKeys: keys})
	if err != nil {
		t.Fatal(err)
	}
	reset := now.Add(4 * time.Hour)
	req := apiv1.TurnResult{Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "rate_limit", ResetAt: core.TS(reset)}}
	if _, err := p.srv.recordTurnResultAttempt("refused", "attempt", req); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, "web:b", "other", "u", "other folder", now)
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 0 {
		t.Fatal("provider callback did not immediately block the instance")
	}
	tc, _ := db.GetTurnContext("refused")
	if tc.State != store.TurnRunning || tc.RunReturned || !tc.NextAttemptAt.Equal(reset) {
		t.Fatalf("callback released execution or lost reset: %+v", tc)
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	for _, frame := range []apiv1.TurnResult{req, {Status: "success"}} {
		if _, err := p.srv.recordTurnResultAttempt("refused", "attempt", frame); err == nil {
			t.Fatal("refused attempt accepted a later callback")
		}
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if after != control || !p.srv.callbackClosed(tc) {
		t.Fatal("duplicate refusal changed episode or callback marker stayed open")
	}
	fns := p.srv.buildRoutdFns(turnMCP{folder: "a", turnID: tc.TurnID, attempt: tc.AttemptID})
	if _, err := fns.CheckTurn("a", tc.TurnID); err == nil {
		t.Fatal("refused attempt accepted a delivery check")
	}
	now = reset.Add(time.Minute)
	l.q.busyWait.Delete("web:b")
	processPauseChat(t, l, "web:b")
	if len(p.runs) != 0 {
		t.Fatal("provider callback admitted another run before runtime return")
	}
	deferred, err := l.q.complete(l, tc, runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, nil, turnWork{})
	if err != nil || !deferred {
		t.Fatalf("provider terminal bit defeated retry: %v %v", deferred, err)
	}
	tc, _ = db.GetTurnContext("refused")
	if tc.State != store.TurnPendingRetry || tc.RetryCount != 1 || !tc.NextAttemptAt.Equal(retryAt(now, tc.Trigger, 1)) || db.GetAgentCursor("web:a") != tc.InputLast {
		t.Fatalf("provider deferral: %+v", tc)
	}
	providerDue(t, l, "refused")
	after, _, _ = db.store().ReadTurnControl(t.Context())
	if after.Phase != "closed" || len(p.runs) != 1 {
		t.Fatalf("runtime recovery: %+v runs=%d", after, len(p.runs))
	}
}

func TestTurnControlProviderExhaustion(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(fmt.Sprint(scheduled), func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "outage", "outage", "outage", "outage")
			l.maxTurnRetry = 3
			now := time.Now().UTC()
			l.q.now = func() time.Time { return now }
			sender := "u"
			if scheduled {
				sender = "timed"
			}
			pauseInput(t, db, "web:a", "refused", sender, "saved", now)
			processPauseChat(t, l, "web:a")
			for count := 1; count <= 3; count++ {
				tc, _ := db.GetTurnContext("refused")
				if tc.State != store.TurnPendingRetry || tc.RetryCount != count || !tc.NextAttemptAt.Equal(retryAt(now, sender, count)) {
					t.Fatalf("retry %d not pending with shared delay: %+v", count, tc)
				}
				now = tc.NextAttemptAt
				providerDue(t, l, "refused")
			}
			tc, _ := db.GetTurnContext("refused")
			control, _, _ := db.store().ReadTurnControl(t.Context())
			pause, _ := db.store().ReadModelPause(t.Context(), "a")
			if len(p.runs) != 4 || tc.State != store.TurnDone || tc.RetryCount != 3 || pause == nil || control.Phase != "open" || control.ProbeTurnID != "" || !control.NextProbeAt.Equal(now.Add(time.Hour)) {
				t.Fatalf("exhaustion: runs=%d turn=%+v control=%+v pause=%+v", len(p.runs), tc, control, pause)
			}
			pauseInput(t, db, "web:b", "next", "v", "next", now)
			processPauseChat(t, l, "web:b")
			if len(p.runs) != 4 {
				t.Fatal("exhaustion bypassed hourly cooldown")
			}
			now = control.NextProbeAt
			l.q.now = func() time.Time { return now }
			l.q.busyWait.Delete("web:b")
			processPauseChat(t, l, "web:b")
			control, _, _ = db.store().ReadTurnControl(t.Context())
			if len(p.runs) != 5 || control.Phase != "closed" {
				t.Fatalf("successor did not recover: %+v", control)
			}
		})
	}
}

func TestTurnControlProviderCallbackRestart(t *testing.T) {
	db, _, p, dl := pauseFixture(t)
	now := time.Now().UTC()
	pauseInput(t, db, "web:a", "refused", "u", "saved", now)
	msg, _ := db.MessageByID("web:a", "refused")
	keys, _ := turnMessageKeys([]core.Message{msg})
	_, err := db.store().OpenTurnContext("refused", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "attempt", InputLast: store.MessageCursor(msg), InputKeys: keys})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.SetTurnRunID("refused", "captured")
	_, err = p.srv.recordTurnResultAttempt("refused", "attempt", apiv1.TurnResult{Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewLoop(db, p, LoopConfig{Deliver: dl, MaxTurnRetry: 3})
	t.Cleanup(restarted.StopQueue)
	p.srv = NewServer(db, restarted, dl, nil, 0, "")
	p.stopErr = errors.New("runed unavailable")
	if err := restarted.q.recover(restarted, true); err == nil {
		t.Fatal("failed reconciliation released execution")
	}
	pauseInput(t, db, "web:b", "other", "u", "other", now)
	processPauseChat(t, restarted, "web:b")
	if len(p.runs) != 0 {
		t.Fatal("restart admitted before reconciliation")
	}
	p.stopErr = nil
	if err := restarted.q.recover(restarted, false); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("refused")
	if tc.State != store.TurnPendingRetry || tc.RetryCount != 1 || len(p.kills) != 2 {
		t.Fatalf("callback recovery: %+v kills=%v", tc, p.kills)
	}
	if err := restarted.q.recover(restarted, false); err != nil {
		t.Fatal(err)
	}
	providerDue(t, restarted, "refused")
	if len(p.runs) != 1 {
		t.Fatal("recovery replayed more than once")
	}
}

func TestTurnControlProviderTransportFailure(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "outage")
	p.runErr = &runedv1.APIError{Status: 502, Code: "gateway_error"}
	p.stopErr = errors.New("kill unconfirmed")
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	if _, err := l.processGroupMessages("web:a"); !errors.Is(err, p.stopErr) {
		t.Fatalf("provider transport did not reconcile: %v", err)
	}
	tc, _ := db.GetTurnContext("refused")
	if tc.State != store.TurnRunning || tc.RetryCount != 0 || len(p.kills) != 1 {
		t.Fatalf("uncertain provider execution deferred: %+v kills=%v", tc, p.kills)
	}
	p.stopErr = nil
	if err := l.q.recover(l, false); err != nil {
		t.Fatal(err)
	}
	tc, _ = db.GetTurnContext("refused")
	if tc.State != store.TurnPendingRetry || tc.RetryCount != 1 || len(p.kills) != 2 {
		t.Fatalf("confirmed provider execution not deferred: %+v", tc)
	}
}

func TestTurnControlManualClear(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	control, _, _ := db.store().ReadTurnControl(t.Context())
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	ws := &store.WriteSet{}
	if err := db.store().ClearModelPauseInto(ws, *pause, errors.New("changed")); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	processPauseChat(t, l, "web:a")
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if control != after || len(p.runs) != 1 {
		t.Fatal("metadata clear changed breaker")
	}
	providerDue(t, l, "refused")
	if len(p.runs) != 2 {
		t.Fatal("metadata clear lost deferred input")
	}
}

func TestModelPauseFailureBeforeGuestSubmissionDoesNotProveRecovery(t *testing.T) {
	for _, sample := range []struct {
		name    string
		outcome runedv1.RunOutcome
	}{
		{"exhausted", runedv1.RunOutcome{Outcome: runedv1.OutcomeError}},
		{"terminal", runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}},
		{"silent", runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "outage")
			l.maxTurnRetry = 3
			pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
			processPauseChat(t, l, "web:a")
			p.noSubmission, p.localOutcome = true, sample.outcome
			for count := 1; count <= 3; count++ {
				providerDue(t, l, "refused")
				tc, _ := db.GetTurnContext("refused")
				if tc.State == store.TurnDone {
					break
				}
				if tc.RetryCount != count+1 || tc.State != store.TurnPendingRetry {
					t.Fatalf("retry %d not deferred: %+v", count, tc)
				}
			}
			tc, _ := db.GetTurnContext("refused")
			control, _, _ := db.store().ReadTurnControl(t.Context())
			if tc.State != store.TurnDone || control.Phase != "open" {
				t.Fatalf("failed probe proved recovery: turn=%+v control=%+v", tc, control)
			}
		})
	}
}

func TestTurnControlProviderSavedReplay(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	jid := "telegram:group/saved"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	pauseInput(t, db, jid, "refused", "u", "saved", time.Now())
	processPauseChat(t, l, jid)
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "b"}})
	pauseInput(t, db, jid, "later", "u", "later", time.Now().Add(time.Second))
	providerDue(t, l, "refused")
	if len(p.runs) != 2 || p.runs[1].Folder != "a" || strings.Contains(p.runs[1].MessageBatch, "later") {
		t.Fatalf("saved provider binding changed: %+v", p.runs)
	}
}

func TestTurnControlProviderDeadline(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			db, l, p, _ := pauseFixture(t)
			pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
			msg, _ := db.MessageByID("web:a", "refused")
			keys, _ := turnMessageKeys([]core.Message{msg})
			_, _ = db.store().OpenTurnContext("refused", "a", "", "web:a", "u", "", &store.TurnAdmission{InputLast: store.MessageCursor(msg), InputKeys: keys})
			tc, _ := db.GetTurnContext("refused")
			_, err := p.srv.recordTurnResultAttempt(tc.TurnID, tc.AttemptID, apiv1.TurnResult{Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}})
			if err != nil {
				t.Fatal(err)
			}
			tc, _ = db.GetTurnContext("refused")
			if deferred, err := l.q.complete(l, tc, runedv1.RunOutcome{}, failure, turnWork{}); err != nil || deferred {
				t.Fatalf("stop/deadline retried: %v %v", deferred, err)
			}
		})
	}
}

func TestTurnControlProviderEpisodeFence(t *testing.T) {
	db, l, _, _ := pauseFixture(t, "outage")
	_, _ = db.store().OpenTurnContext("older", "b", "", "web:b", "u", "", nil)
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	saved, _ := db.GetTurnContext("refused")
	l.q.now = func() time.Time { return saved.NextAttemptAt }
	admission := &store.TurnAdmission{AttemptID: "probe"}
	work, ok, err := l.q.admit(l, saved, admission, &saved)
	if err != nil || !ok {
		t.Fatalf("probe admission: %v %v", ok, err)
	}
	other, _ := db.GetTurnContext("older")
	if _, err := l.q.refuse(l, other, "outage", time.Time{}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.store().ReadTurnControl(t.Context())
	probe, _ := db.GetTurnContext("refused")
	if _, err := db.store().RecordTurnResult(probe.Folder, probe.TurnID, "", "success", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.q.complete(l, probe, runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil, work); err != nil {
		t.Fatal(err)
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if after.Phase != before.Phase || after.Episode != before.Episode || after.NextProbeAt != before.NextProbeAt {
		t.Fatal("successful provider probe cleared a newer refusal episode")
	}
}

func TestTurnControlProviderEarlySubmission(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "outage")
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	saved, _ := db.GetTurnContext("refused")
	l.q.now = func() time.Time { return saved.NextAttemptAt }
	work, ok, err := l.q.admit(l, saved, &store.TurnAdmission{AttemptID: "probe"}, &saved)
	if err != nil || !ok {
		t.Fatalf("probe admission: %v %v", ok, err)
	}
	p.srv.replyForTest(t, "refused", "web:a", "recovered")
	if _, err := p.srv.recordTurnResultAttempt("refused", "probe", apiv1.TurnResult{Status: "success"}); err != nil {
		t.Fatal(err)
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	if control.Phase != "probe" {
		t.Fatal("early submission proved provider recovery")
	}
	tc, _ := db.GetTurnContext("refused")
	if _, err := l.q.complete(l, tc, runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil, work); err != nil {
		t.Fatal(err)
	}
	control, _, _ = db.store().ReadTurnControl(t.Context())
	if control.Phase != "closed" {
		t.Fatal("runtime confirmation did not clear the breaker")
	}
}

func TestTurnControlProviderLegacyMigration(t *testing.T) {
	for _, captured := range []bool{false, true} {
		for _, spent := range []int{0, 2, 3} {
			t.Run(fmt.Sprintf("captured=%t/spent=%d", captured, spent), func(t *testing.T) {
				db, l, p, _ := pauseFixture(t)
				jid := "telegram:group/legacy"
				doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
				now := time.Now().UTC()
				l.q.now = func() time.Time { return now }
				pauseInput(t, db, jid, "old", "u", "saved", now)
				msg, _ := db.MessageByID(jid, "old")
				_, _ = db.store().OpenTurnContext("old", "a", "", jid, "u", "",
					&store.TurnAdmission{AttemptID: "legacy", InputLast: store.MessageCursor(msg), ProbeEpisode: core.TS(now)})
				state := store.TurnHeld
				metadata := map[string]string{"reason": "rate_limit", "evidence": "rate_limit", "opened_at": core.TS(now), "attempts": "1", "next_attempt_at": core.TS(now.Add(2 * time.Hour))}
				if captured {
					state = store.TurnRunning
					metadata["probe_turn_id"], metadata["probe_attempt_id"] = "old", "legacy"
				}
				doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "b"}})
				if err := db.SetStickyTopic(jid, "changed"); err != nil {
					t.Fatal(err)
				}
				attrs := map[string]string{"state": state, "provider_failure_attempt": "legacy", "run_id": "captured"}
				if spent > 0 {
					attrs["retry_count"] = fmt.Sprint(spent)
				}
				if err := db.store().WriteEntities(t.Context(), nil,
					objstore.Write{Op: objstore.Update, Entity: store.TurnContexts, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "old"}, Attrs: attrs},
					objstore.Write{Op: objstore.Put, Entity: store.ModelPauses, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "a"}, Attrs: metadata}); err != nil {
					t.Fatal(err)
				}
				p.stopErr = errors.New("runed unavailable")
				if err := l.q.recover(l, true); err == nil {
					t.Fatal("legacy reconciliation failure was ignored")
				}
				unresolved, _ := db.GetTurnContext("old")
				if unresolved.RetryCount != spent || len(unresolved.InputKeys) != 0 {
					t.Fatalf("unresolved legacy execution migrated: %+v", unresolved)
				}
				p.stopErr = nil
				if err := l.q.recover(l, false); err != nil {
					t.Fatal(err)
				}
				tc, _ := db.GetTurnContext("old")
				if tc.RetryCount != min(spent+1, 3) || len(tc.InputKeys) != 1 || tc.Folder != "a" || tc.Topic != "" || db.GetAgentCursor(jid) != tc.InputLast ||
					(spent < 3 && tc.State != store.TurnPendingRetry) || (spent == 3 && tc.State != store.TurnDone) {
					t.Fatalf("legacy migration lost budget or binding: %+v", tc)
				}
				if err := l.q.recover(l, false); err != nil {
					t.Fatal(err)
				}
				after, _ := db.GetTurnContext("old")
				if after.RetryCount != tc.RetryCount || len(p.kills) != 2 {
					t.Fatalf("migration repeated refusal reservation: %+v kills=%v", after, p.kills)
				}
			})
		}
	}
}

package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestCapacityBusyRejectsNewerAttempt(t *testing.T) {
	db, loop, _ := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	msg := core.Message{ID: "input", ChatJID: "web:demo", Sender: "u", Content: "question", Timestamp: time.Now()}
	if err := db.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	keys, err := turnMessageKeys([]core.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	loop.runner = runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		ok, err := db.store().OpenTurnContext(msg.ID, "demo", "", msg.ChatJID, msg.Sender, "", &store.TurnAdmission{
			AttemptID: "newer", InputLast: store.MessageCursor(msg), InputKeys: keys,
		})
		if err != nil || !ok {
			t.Fatalf("replace attempt: %v %v", ok, err)
		}
		return runedv1.RunOutcome{Busy: true}, nil
	})
	_, _, _, err = loop.runTurn("demo", "", msg.ChatJID, msg.ID, false, []core.Message{msg}, nil)
	if !errors.Is(err, store.ErrRefused) {
		t.Fatalf("stale Busy: %v", err)
	}
	tc, ok, err := db.store().ReadTurnContext(t.Context(), msg.ID)
	if err != nil || !ok || tc.State != store.TurnRunning || tc.AttemptID != "newer" || tc.CapacityFailures != 0 || !tc.NextAttemptAt.IsZero() || db.GetAgentCursor(msg.ChatJID) != "" {
		t.Fatalf("newer attempt changed: %+v, exists=%v, err=%v", tc, ok, err)
	}
}

func TestCapacityProbeRestartRearmsWithoutKill(t *testing.T) {
	db, loop, runner, _ := pauseFixture(t)
	jid := "web:a"
	pauseInput(t, db, jid, "input", "u", "question", time.Now())
	runner.busy = true
	processPauseChat(t, loop, jid)
	tc, ok, err := db.store().ReadTurnContext(t.Context(), "input")
	if err != nil || !ok || tc.State != store.TurnWaitingCapacity {
		t.Fatalf("wait: %+v, %v", tc, err)
	}
	probe := store.TurnControlState{Phase: "probe", Episode: "episode", ProbeTurnID: tc.TurnID, ProbeAttemptID: tc.AttemptID, NextProbeAt: tc.NextAttemptAt}
	if ok, err := db.store().WriteTurnControl(t.Context(), nil, probe); err != nil || !ok {
		t.Fatalf("probe: %v %v", ok, err)
	}
	if err := loop.q.recover(loop, true); err != nil {
		t.Fatal(err)
	}
	after, exists, err := db.store().ReadTurnControl(t.Context())
	current, ok, readErr := db.store().ReadTurnContext(t.Context(), tc.TurnID)
	if err != nil || !exists || after.Phase != "open" || after.ProbeAttemptID != "" || after.ProbeTurnID != tc.TurnID || after.Episode != probe.Episode || !after.NextProbeAt.Equal(probe.NextProbeAt) || readErr != nil || !ok || current.State != store.TurnWaitingCapacity || current.AttemptID != tc.AttemptID || current.CapacityFailures != tc.CapacityFailures || !current.NextAttemptAt.Equal(tc.NextAttemptAt) || len(runner.kills) != 0 || runner.stops != 0 {
		t.Fatalf("restart: control=%+v turn=%+v kills=%v stops=%d errors=%v/%v", after, current, runner.kills, runner.stops, err, readErr)
	}
}

package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestTurnControlSavesInputBeforeRun(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	input := core.Message{ID: "input", ChatJID: "web:chat", Sender: "user", Timestamp: now}
	ambient := core.Message{ID: "ambient", ChatJID: "web:chat", Sender: "other", Timestamp: now.Add(-time.Second)}
	for _, msg := range []core.Message{input, ambient} {
		if err := db.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	runner := runnerFn(func(ctx context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		tc, ok, err := db.store().ReadTurnContext(ctx, input.ID)
		if err != nil || !ok {
			t.Fatalf("read binding: ok=%v err=%v", ok, err)
		}
		inputKey, _ := store.MessageKey(input.ChatJID, core.TS(input.Timestamp), input.ID)
		ambientKey, _ := store.MessageKey(ambient.ChatJID, core.TS(ambient.Timestamp), ambient.ID)
		if len(tc.InputKeys) != 1 || tc.InputKeys[0] != inputKey || len(tc.AmbientKeys) != 1 || tc.AmbientKeys[0] != ambientKey {
			t.Fatalf("input keys=%v ambient keys=%v", tc.InputKeys, tc.AmbientKeys)
		}
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	if _, _, _, _, err := loop.runTurn("demo", "", input.ChatJID, input.ID, false, []core.Message{input}, []core.Message{ambient}); err != nil {
		t.Fatal(err)
	}
}

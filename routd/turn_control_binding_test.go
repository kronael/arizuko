package routd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestMCPSetupFailureSettlesTurn(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "fresh"
		if saved {
			name = "saved"
		}
		t.Run(name, func(t *testing.T) {
			var db *DB
			var loop *Loop
			var runner *recRunner
			var tc TurnContext
			if saved {
				db, loop, runner, tc, _ = deferredFixture(t)
				loop.q.now = func() time.Time { return tc.NextAttemptAt }
			} else {
				db, loop, runner = recLoop(t)
				if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
					t.Fatal(err)
				}
				msg := core.Message{ID: "fresh", ChatJID: "web:demo", Sender: "user", Content: "hello", Timestamp: time.Now().UTC()}
				if err := db.PutMessage(msg); err != nil {
					t.Fatal(err)
				}
				tc = TurnContext{TurnID: msg.ID, ChatJID: msg.ChatJID, Folder: "demo", InputLast: store.MessageCursor(msg)}
			}
			base := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			loop.folders.IpcDir = base
			loop.BindServer(NewServer(db, loop, nil, nil, 0, ""))
			var err error
			if saved {
				_, err = loop.replay(tc.TurnID)
			} else {
				_, err = loop.processGroupMessages(tc.ChatJID)
			}
			if !errors.Is(err, ErrDependencyDown) {
				t.Errorf("setup error = %v", err)
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				t.Errorf("missing filesystem cause: %v", err)
			}
			if len(runner.runs) != 0 {
				t.Errorf("runner started: %+v", runner.runs)
			}
			current, ok := db.GetTurnContext(tc.TurnID)
			if !ok || current.State == store.TurnRunning {
				t.Errorf("turn not settled: %+v", current)
			}
			if current.Folder != tc.Folder || current.ChatJID != tc.ChatJID || current.InputLast != tc.InputLast {
				t.Errorf("binding changed: %+v", current)
			}
			wantCursor := ""
			if saved {
				wantCursor = tc.InputLast
			}
			if got := db.GetAgentCursor(tc.ChatJID); got != wantCursor {
				t.Errorf("cursor = %q, want %q", got, wantCursor)
			}
			loop.q.mu.Lock()
			reservations := len(loop.q.reservations)
			loop.q.mu.Unlock()
			if reservations != 0 {
				t.Errorf("reservations = %d", reservations)
			}
		})
	}
}

func (l *Loop) runTurn(folder, topic, chatJID, turnID string, addressed bool, trigger, ambient []core.Message) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	return l.runBoundTurn(folder, topic, chatJID, turnID, addressed, trigger, ambient, nil, store.MessageCursor(trigger[len(trigger)-1]))
}

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

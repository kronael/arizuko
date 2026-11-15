package runed

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// A group whose agent names an executable the guest does not have: guest init
// exits 78, runed maps that to a terminal error, and routd over the real runed
// API runs the turn once, tells the chat once and never dispatches again
// (specs/7/57-group-handler.md, acceptance 3). turnResult is the real mapping a
// guest's exit frame goes through; the guest itself needs KVM.
func TestMissingHandlerFailsTheTurnOnce(t *testing.T) {
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.NewOver(db.Obj()).SetGroupSettings("demo", map[string]json.RawMessage{"agent": json.RawMessage(`"missing"`)}); err != nil {
		t.Fatal(err)
	}
	var launches atomic.Int32
	var agent atomic.Value
	runtime := FakeRuntime{Fn: func(_ context.Context, turn Turn) RunResult {
		launches.Add(1)
		agent.Store(turn.Env["ARIZUKO_AGENT"])
		return turnResult(turnEnd{code: 78, framed: true}, time.Second, "console",
			"guest-init: exec /usr/local/bin/missing: no such file or directory")
	}}
	runDB, manager := newMgr(t, runtime, 1)
	httpServer := httptest.NewServer(NewServer(manager, runDB, nil).Handler())
	t.Cleanup(httpServer.Close)
	loop := routd.NewLoop(db, runedv1.NewClient(httpServer.URL, "", time.Minute), routd.LoopConfig{})
	t.Cleanup(loop.StopQueue)
	if err := db.PutMessage(core.Message{ID: "missing-handler", ChatJID: "web:demo", Sender: "user",
		Content: "question", Timestamp: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	loop.Enqueue("web:demo")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if tc, ok := db.GetTurnContext("missing-handler"); ok && tc.State == store.TurnDone && tc.RunReturned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not end")
		}
		time.Sleep(50 * time.Millisecond)
	}
	loop.Enqueue("web:demo")
	time.Sleep(time.Second)

	tc, _ := db.GetTurnContext("missing-handler")
	if tc.RetryCount != 0 || launches.Load() != 1 || agent.Load() != "missing" {
		t.Fatalf("turn=%+v launches=%d agent=%v, want one run of agent missing and no retry", tc, launches.Load(), agent.Load())
	}
	msgs, err := db.MessagesSince("web:demo", "")
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, m := range msgs {
		if m.Verb == "status" {
			notices++
			if !strings.Contains(m.Content, "Automatic retries stopped") {
				t.Fatalf("notice %q is not a final failure", m.Content)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("failure notices = %d, want 1: %+v", notices, msgs)
	}
}

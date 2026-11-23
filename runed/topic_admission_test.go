package runed

import (
	"context"
	"sync"
	"testing"
	"time"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// liveRuns starts n runs that each hold their slot until release closes, and
// returns once all n are live. It records the MCP socket each turn reaches.
type liveRuns struct {
	started chan string
	release chan struct{}
	wg      sync.WaitGroup
}

func newLiveRuns() *liveRuns {
	return &liveRuns{started: make(chan string, 8), release: make(chan struct{})}
}

func (r *liveRuns) runtime() Runtime {
	return FakeRuntime{Fn: func(_ context.Context, t Turn) RunResult {
		r.started <- turnSocket(t)
		<-r.release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
}

func (r *liveRuns) start(t *testing.T, mgr *Manager, req runedv1.RunRequest) string {
	t.Helper()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		_, _ = mgr.Run(context.Background(), req)
	}()
	select {
	case socket := <-r.started:
		return socket
	case <-time.After(5 * time.Second):
		t.Fatalf("run %s/%s never started", req.Folder, req.Topic)
		return ""
	}
}

func (r *liveRuns) stop() {
	close(r.release)
	r.wg.Wait()
}

func topicRun(topic, turnID string) runedv1.RunRequest {
	return runedv1.RunRequest{Packages: []string{"core"}, Folder: types.Folder("repo"),
		Topic: topic, TurnID: turnID, MessageBatch: "m"}
}

// BUGS L920: two topics of one folder each get a guest at once, each reaching
// its own MCP socket; a second run of a live topic is refused busy.
func TestTwoTopicsOfOneFolderRunAtOnce(t *testing.T) {
	runs := newLiveRuns()
	_, mgr := newMgr(t, runs.runtime(), 5)
	defer runs.stop()

	first := runs.start(t, mgr, topicRun("pr-1", "turn-a"))
	second := runs.start(t, mgr, topicRun("pr-2", "turn-b"))
	if first == "" || first == second {
		t.Fatalf("two turns of one folder share the MCP socket %q / %q", first, second)
	}

	out, err := mgr.Run(context.Background(), topicRun("pr-1", "turn-c"))
	if err != nil || !out.Busy {
		t.Fatalf("second run of a live topic: out=%+v err=%v, want busy", out, err)
	}
}

// A hold takes the whole folder: it is refused beside any live run, and no
// run of any topic starts beside it.
func TestHoldTakesEveryTopicOfItsFolder(t *testing.T) {
	runs := newLiveRuns()
	_, mgr := newHoldMgr(t, runs.runtime(), 5, time.Minute)

	runs.start(t, mgr, topicRun("pr-1", "turn-a"))
	held, err := mgr.Hold(context.Background(), types.Folder("repo"), "restore")
	if err != nil || !held.Busy {
		t.Fatalf("hold beside a live run: %+v err=%v, want busy", held, err)
	}
	runs.stop()

	_, mgr = newHoldMgr(t, FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		t.Error("a run started beside a hold")
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}, 5, time.Minute)
	if held, err := mgr.Hold(context.Background(), types.Folder("repo"), "restore"); err != nil || held.Busy {
		t.Fatalf("hold of an idle folder: %+v err=%v", held, err)
	}
	out, err := mgr.Run(context.Background(), topicRun("pr-2", "turn-b"))
	if err != nil || !out.Busy {
		t.Fatalf("run beside a hold: out=%+v err=%v, want busy", out, err)
	}
}

// StopFolder with a turn id kills that turn's run and leaves the other
// topic's run live; without one it kills every live run of the folder.
func TestStopFolderByTurnKillsOnlyThatRun(t *testing.T) {
	runs := newLiveRuns()
	db, mgr := newMgr(t, runs.runtime(), 5)
	defer runs.stop()
	runs.start(t, mgr, topicRun("pr-1", "turn-a"))
	runs.start(t, mgr, topicRun("pr-2", "turn-b"))

	runID, killed, err := mgr.StopFolder(t.Context(), "repo", "turn-a")
	if err != nil || !killed {
		t.Fatalf("stop turn-a: killed=%v err=%v", killed, err)
	}
	if sp, _ := db.SpawnByRunID(t.Context(), runID); sp.TurnID != "turn-a" {
		t.Fatalf("killed run %s belongs to turn %q, want turn-a", runID, sp.TurnID)
	}
	live, err := db.LiveSpawnsInFolder(t.Context(), "repo")
	if err != nil || len(live) != 1 || live[0].TurnID != "turn-b" {
		t.Fatalf("live after stopping turn-a: %+v err=%v, want only turn-b", live, err)
	}

	if _, killed, err := mgr.StopFolder(t.Context(), "repo", ""); err != nil || !killed {
		t.Fatalf("stop the folder: killed=%v err=%v", killed, err)
	}
	if live, err := db.LiveSpawnsInFolder(t.Context(), "repo"); err != nil || len(live) != 0 {
		t.Fatalf("live after stopping the folder: %+v err=%v", live, err)
	}
}

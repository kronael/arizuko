package runed

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// blockingRunner is a container.Runner that records the spawned Input, blocks
// until released, and returns a fixed Output — standing in for a live
// container without docker.
type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	last    atomic.Pointer[container.Input]
	out     container.Output
}

func (b *blockingRunner) Run(_ *core.Config, _ *groupfolder.Resolver, in container.Input) container.Output {
	cp := in
	b.last.Store(&cp)
	close(b.started)
	<-b.release
	return b.out
}

// TestProdSteerPath exercises the PRODUCTION steer wiring: dockerRuntime.Run
// registers the steer closure (IPC write + SIGUSR1) before spawning, so a
// concurrent POST /v1/runs for the busy folder steers into the live
// container (steered:true) instead of a fresh spawn. The docker `kill`
// SIGUSR1 is faked; the IPC write hits a real tmp dir (spec 5/P § steer).
func TestProdSteerPath(t *testing.T) {
	ipcDir := t.TempDir()
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: ipcDir}
	runner := &blockingRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
		out:     container.Output{Status: "success", Result: "replied", HadOutput: true, ExitCode: 0, MessageCount: 2, NewSessionID: "s-prod"},
	}
	var signaled int32
	rt := &dockerRuntime{
		cfg: &core.Config{}, folders: folders, runner: runner, fed: NewFederator("http://routd.invalid"),
		signal: func(name string) error { atomic.AddInt32(&signaled, 1); return nil },
	}

	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := NewManager(db, rt, NewStaticBroker("jws", "jti"), ManagerConfig{Instance: "test", MaxConcurrent: 5})

	done := make(chan runedv1.RunOutcome, 1)
	go func() {
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "acme/eng", MessageBatch: "first"})
		done <- out
	}()
	<-runner.started // container is "live"; steer closure is registered.

	steer, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "acme/eng", MessageBatch: "second batch"})
	if !steer.Steered {
		t.Fatalf("prod second Run steered=%v want true (out=%+v)", steer.Steered, steer)
	}
	if atomic.LoadInt32(&signaled) != 1 {
		t.Fatalf("SIGUSR1 sent %d times, want 1", signaled)
	}
	// the steered batch was written as an IPC input file for the live container.
	in, _ := os.ReadDir(filepath.Join(ipcDir, "acme", "eng", "input"))
	if len(in) != 1 {
		t.Fatalf("ipc input files=%d want 1 (the steered batch)", len(in))
	}

	close(runner.release)
	first := <-done
	// the live runner spawned exactly once with the Manager's pinned name.
	if got := runner.last.Load(); got == nil || got.Name == "" {
		t.Fatalf("runner Input.Name not set from RunSpec.ContainerName: %+v", got)
	}
	// exit_code + message_count flow from container.Output → session_log.
	if first.Outcome != runedv1.OutcomeOK {
		t.Fatalf("first outcome=%q want ok", first.Outcome)
	}
	sessions, _ := db.RecentSessions("acme/eng", 10)
	if len(sessions) != 1 || sessions[0].MessageCount != 2 {
		t.Fatalf("session_log message_count not populated from Output: %+v", sessions)
	}
}

// TestExitCodeMessageCountFlow: dockerRuntime maps container.Output's
// ExitCode + MessageCount into the RunResult → spawns/session_log
// (bugs.md should-fix runtimes.go:66).
func TestExitCodeMessageCountFlow(t *testing.T) {
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: t.TempDir()}
	runner := &blockingRunner{
		started: make(chan struct{}), release: make(chan struct{}),
		out: container.Output{Status: "error", ExitCode: 137, MessageCount: 5, Error: "killed"},
	}
	close(runner.release) // don't block.
	rt := &dockerRuntime{cfg: &core.Config{}, folders: folders, runner: runner,
		fed: NewFederator("http://routd.invalid"), signal: func(string) error { return nil }}

	res := rt.Run(context.Background(), RunSpec{
		RunID: "run_x", Folder: "demo", ContainerName: "arizuko-test-demo-1", MessageBatch: "m",
		RegisterSteer: func(func(string) bool) {},
	})
	if res.ExitCode != 137 || res.MessageCount != 5 {
		t.Fatalf("RunResult exit=%d msgs=%d want 137,5", res.ExitCode, res.MessageCount)
	}
	if res.Outcome != runedv1.OutcomeError {
		t.Fatalf("outcome=%q want error", res.Outcome)
	}
}

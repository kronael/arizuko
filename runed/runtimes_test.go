package runed

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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
		cfg: &core.Config{}, folders: folders, runner: runner,
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
		signal: func(string) error { return nil }}

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

// TestDockerRuntimeRunTTLKill: dockerRuntime.Run enforces RunSpec.RunTTL as a
// kill-deadline FROM WITHIN the run path. A run that outlives runTTL is Kill'd
// by name; the kill is armed only once the run is underway and stops when the
// run returns — no detached manager timer racing container creation (gap 1).
func TestDockerRuntimeRunTTLKill(t *testing.T) {
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: t.TempDir()}
	runner := &blockingRunner{
		started: make(chan struct{}), release: make(chan struct{}),
		out: container.Output{Status: "error", Error: "timed out", ExitCode: 137},
	}
	killed := make(chan string, 4)
	rt := &dockerRuntime{
		cfg: &core.Config{}, folders: folders, runner: runner,
		signal: func(string) error { return nil },
		// The TTL watcher fires this; capture the name and release the run so
		// it returns (the prod kill would stop the container, ending the run).
		kill: func(name string) error {
			select {
			case killed <- name:
			default:
			}
			select {
			case <-runner.release:
			default:
				close(runner.release)
			}
			return nil
		},
	}

	res := rt.Run(context.Background(), RunSpec{
		RunID: "run_ttl", Folder: "demo", ContainerName: "arizuko-test-demo-ttl",
		MessageBatch: "m", RunTTL: 20 * time.Millisecond,
		RegisterSteer: func(func(string) bool) {},
	})
	select {
	case name := <-killed:
		if name != "arizuko-test-demo-ttl" {
			t.Fatalf("runTTL Kill got name %q want arizuko-test-demo-ttl", name)
		}
	default:
		t.Fatal("runTTL deadline never Kill'd the over-ceiling container")
	}
	if res.Outcome != runedv1.OutcomeError {
		t.Fatalf("outcome=%q want error (the killed run)", res.Outcome)
	}
}

// TestDockerRuntimeRunTTLNoKillOnFastRun: a run that finishes inside runTTL is
// NOT Kill'd, and no kill fires after the run returns — the watcher stops
// deterministically on completion (gap 2, the late-kill race). RunTTL=0 (no
// ceiling) likewise never arms.
func TestDockerRuntimeRunTTLNoKillOnFastRun(t *testing.T) {
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: t.TempDir()}
	runner := &blockingRunner{
		started: make(chan struct{}), release: make(chan struct{}),
		out: container.Output{Status: "success", ExitCode: 0},
	}
	close(runner.release) // run returns immediately.
	var kills int32
	rt := &dockerRuntime{
		cfg: &core.Config{}, folders: folders, runner: runner,
		signal: func(string) error { return nil },
		kill:   func(string) error { atomic.AddInt32(&kills, 1); return nil },
	}

	rt.Run(context.Background(), RunSpec{
		RunID: "run_fast", Folder: "demo", ContainerName: "arizuko-test-demo-fast",
		MessageBatch: "m", RunTTL: 50 * time.Millisecond,
		RegisterSteer: func(func(string) bool) {},
	})
	// Wait past the (already-disarmed) deadline to prove no late kill fires.
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&kills); n != 0 {
		t.Fatalf("fast run got %d kills, want 0 (deadline must not fire after completion)", n)
	}
}

// TestDockerRuntimeCancelKills: when routd drops the POST /v1/runs request
// mid-run (ctx cancelled), armCancel Kills the live container exactly once so
// it doesn't orphan to runTTL while the turn gets re-fed into a second
// container (double execution). The kill fake releases the run so it returns.
func TestDockerRuntimeCancelKills(t *testing.T) {
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: t.TempDir()}
	runner := &blockingRunner{
		started: make(chan struct{}), release: make(chan struct{}),
		out: container.Output{Status: "error", Error: "cancelled", ExitCode: 137},
	}
	var kills int32
	rt := &dockerRuntime{
		cfg: &core.Config{}, folders: folders, runner: runner,
		signal: func(string) error { return nil },
		kill: func(string) error {
			atomic.AddInt32(&kills, 1)
			select {
			case <-runner.release:
			default:
				close(runner.release)
			}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan RunResult, 1)
	go func() {
		done <- rt.Run(ctx, RunSpec{
			RunID: "run_cancel", Folder: "demo", ContainerName: "arizuko-test-demo-cancel",
			MessageBatch: "m", RegisterSteer: func(func(string) bool) {},
		})
	}()
	<-runner.started // container is live and blocked in the runner.
	cancel()         // routd dropped the request.

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not Kill the live container; Run never returned")
	}
	if n := atomic.LoadInt32(&kills); n != 1 {
		t.Fatalf("cancel fired %d kills, want exactly 1", n)
	}
}

// TestContainerConfigToGroupConfig: the opaque RunRequest.ContainerConfig map
// round-trips into container.Input.Config (core.GroupConfig) via JSON, and the
// per-group Model override flows onto Input.Model. routd owns the MCP socket
// in-process, so runed spawns with ExternalMCP=true (no in-process ServeMCP).
func TestContainerConfigToGroupConfig(t *testing.T) {
	folders := &groupfolder.Resolver{GroupsDir: t.TempDir(), IpcDir: t.TempDir()}
	runner := &blockingRunner{
		started: make(chan struct{}), release: make(chan struct{}),
		out: container.Output{Status: "success", ExitCode: 0},
	}
	close(runner.release)
	rt := &dockerRuntime{cfg: &core.Config{}, folders: folders, runner: runner,
		signal: func(string) error { return nil }}

	rt.Run(context.Background(), RunSpec{
		RunID: "run_cfg", Folder: "demo", ContainerName: "arizuko-test-demo-cfg",
		MessageBatch: "m", Model: "claude-opus-4-8",
		ContainerConfig: map[string]any{"MaxChildren": 3, "Timeout": int64(90 * time.Second)},
		RegisterSteer:   func(func(string) bool) {},
	})

	in := runner.last.Load()
	if in == nil {
		t.Fatal("runner never spawned")
	}
	if !in.ExternalMCP {
		t.Fatal("Input.ExternalMCP=false want true (routd owns the socket)")
	}
	if in.Model != "claude-opus-4-8" {
		t.Fatalf("Input.Model=%q want claude-opus-4-8", in.Model)
	}
	if in.Config.MaxChildren != 3 || in.Config.Timeout != 90*time.Second {
		t.Fatalf("Input.Config=%+v want MaxChildren=3 Timeout=90s", in.Config)
	}
}

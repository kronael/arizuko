package runed

// docker.go holds the production docker-execution lifecycle: dockerRuntime
// (the container-spawner), its runTTL kill-deadline watcher, steer-into, and
// the IPC input drop. The test seam (FakeRuntime) lives in runtimes.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// dockerRuntime is the production Runtime: a pure container-spawner. routd
// hosts the agent MCP socket in-process (Input.ExternalMCP), so runed only
// mounts the ipc dir and spawns the per-turn container via container.Run.
// runed still owns the lifecycle envelope: steer-into, runTTL kill, teardown.
type dockerRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	runner  container.Runner
	// db is runed's own store (session_log). Retained for runed-local reads;
	// nil in local-dev.
	db *DB
	// signal SIGUSR1s a running container by name (steer wakeup). Defaults
	// to `docker kill --signal=SIGUSR1`; tests inject a fake so the prod
	// steer path is exercised without docker.
	signal func(name string) error
	// kill stops + removes a container by name (the runTTL deadline + DELETE
	// /v1/runs/{id}). Defaults to docker stop→kill→rm -f; tests inject a fake
	// so the runTTL enforcement path is exercised without docker.
	kill func(name string) error
}

// NewDockerRuntime builds the production Runtime around the docker runner.
// db backs the runed-local read surface (session_log).
func NewDockerRuntime(cfg *core.Config, folders *groupfolder.Resolver, db *DB) Runtime {
	return &dockerRuntime{
		cfg: cfg, folders: folders, runner: container.DockerRunner{}, db: db,
		signal: func(name string) error {
			return exec.Command(container.Bin, "kill", "--signal=SIGUSR1", name).Run()
		},
		kill: dockerKill,
	}
}

// dockerKill stops a live container by name: stop, then docker kill, then
// rm -f (spec 5/P § DELETE /v1/runs/{id}). Idempotent — every step is a
// harmless no-op on an already-exited / never-created container, which is
// what makes the runTTL watcher safe to retry.
func dockerKill(name string) error {
	_ = exec.Command(container.Bin, container.StopContainerArgs(name)...).Run()
	_ = exec.Command(container.Bin, "kill", name).Run()
	_ = exec.Command(container.Bin, "rm", "-f", name).Run()
	return nil
}

// Run spawns one container turn. routd owns the MCP socket in-process
// (ExternalMCP), so runed only mounts the ipc dir — no in-process ServeMCP,
// no federation. Before spawning it registers the steer closure so a
// concurrent POST /v1/runs writes into this live container instead of
// spawning a second (spec 5/P § Steer-into-running-container).
func (d *dockerRuntime) Run(_ context.Context, spec RunSpec) RunResult {
	if spec.RegisterSteer != nil {
		ipcDir, _ := d.folders.IpcPath(spec.Folder)
		spec.RegisterSteer(d.steerInto(ipcDir, spec.ContainerName))
	}
	stopTTL := d.armRunTTL(spec.RunTTL, spec.ContainerName)
	defer stopTTL()
	var gc core.GroupConfig
	if len(spec.ContainerConfig) > 0 {
		b, _ := json.Marshal(spec.ContainerConfig)
		_ = json.Unmarshal(b, &gc)
	}
	out := d.runner.Run(d.cfg, d.folders, container.Input{
		Name:        spec.ContainerName,
		Prompt:      spec.MessageBatch,
		SessionID:   spec.SessionID,
		ChatJID:     spec.ChatJID,
		Folder:      spec.Folder,
		Topic:       spec.Topic,
		MessageID:   spec.TurnID,
		Sender:      spec.TriggerSender,
		Model:       spec.Model,
		Config:      gc,
		ExternalMCP: true,
	})
	return RunResult{
		Outcome:      outcomeFor(out),
		NewSessionID: out.NewSessionID,
		Error:        out.Error,
		ExitCode:     out.ExitCode,
		MessageCount: out.MessageCount,
	}
}

// runTTLPollInterval is how often the runTTL watcher retries Kill after the
// deadline elapses — covers a container that hasn't finished `docker run`
// (not yet killable by name) when the ceiling first fires.
const runTTLPollInterval = 250 * time.Millisecond

// armRunTTL starts the runTTL kill-deadline watcher and returns a stop func
// the caller defers. ttl<=0 disarms (returns a no-op stop). Once ttl elapses
// the watcher Kills the container by name and keeps retrying on an interval
// until the run returns — so a slow `docker run` that wasn't yet killable when
// the deadline first fired is still reaped (fixes the single-shot startup
// race). The stop func closes done and the watcher exits, so NO kill can fire
// after the run completes (fixes the late-kill-after-Stop()==false race). Kill
// is idempotent, making the retries harmless.
func (d *dockerRuntime) armRunTTL(ttl time.Duration, containerName string) func() {
	if ttl <= 0 || containerName == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-time.After(ttl):
		}
		for {
			_ = d.Kill(containerName)
			select {
			case <-done:
				return
			case <-time.After(runTTLPollInterval):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// steerInto returns the steer closure: write the batch as an IPC input file
// into the running container's ipc/<folder>/input/ and SIGUSR1 it (carried
// from queue.SendMessages). Returns false when the SIGUSR1 fails — the
// container already exited (the documented steer race); the Manager then
// falls through to a fresh spawn and the orphaned IPC file is drained by the
// next container at session start.
func (d *dockerRuntime) steerInto(ipcDir, containerName string) func(batch string) bool {
	return func(batch string) bool {
		if batch == "" || ipcDir == "" || containerName == "" {
			return false
		}
		if err := writeIPCInput(ipcDir, batch); err != nil {
			return false
		}
		return d.signal(containerName) == nil
	}
}

// Kill stops a live container by name: stop, then docker kill, then rm -f
// (spec 5/P § DELETE /v1/runs/{id}). Idempotent — killing an already-exited
// container is a harmless no-op exit.
func (d *dockerRuntime) Kill(containerName string) error {
	if containerName == "" {
		return nil
	}
	if d.kill == nil {
		return dockerKill(containerName)
	}
	return d.kill(containerName)
}

// writeIPCInput drops one {type:"message",text} file into the container's
// IPC input dir via temp+rename (atomic; the agent's drainIpcInput picks it
// up). Carried from queue.writeIpcFile.
func writeIPCInput(ipcDir, text string) error {
	inputDir := groupfolder.IpcInputDir(ipcDir)
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%04s.json", time.Now().UnixMilli(), strconv.FormatInt(int64(rand.IntN(1679616)), 36))
	fp := filepath.Join(inputDir, name)
	tmp := fp + ".tmp"
	payload, _ := json.Marshal(map[string]string{"type": "message", "text": text})
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// outcomeFor maps a container exit to the run outcome. Post-flip the agent's
// output goes out-of-band over routd's in-process MCP socket, so runed NEVER
// sees it (cmd.Stdout is io.Discard, HadOutput is never set) — the old
// `!HadOutput && Result==""` → Silent test mis-classified EVERY clean run as
// silent, which routd's queue counts as a failure → the folder's circuit breaker
// tripped after 3 turns. A completed run is OK unless the runner flagged an
// error/timeout (Status=="error" on a crash, or Error set on timeout/spawn-fail).
func outcomeFor(o container.Output) string {
	if o.Status == "error" || o.Error != "" {
		return runedv1.OutcomeError
	}
	return runedv1.OutcomeOK
}

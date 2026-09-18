package runed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	"github.com/kronael/arizuko/obs"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/substrate"
)

// dockerRuntime is the production Runtime: a pure container-spawner. routd
// hosts the agent MCP socket, so runed only mounts the ipc dir and spawns the
// per-turn container via container.Run.
// runed still owns the lifecycle envelope: steer-into, runTTL kill, teardown.
type dockerRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	runner  container.Runner
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
// It reaps any egress proxies a previous runed left behind: an orphan holds a
// live allowlist — possibly an elevated turn's unrestricted one — on a folder
// network where a later spawn could reach it.
func NewDockerRuntime(cfg *core.Config, folders *groupfolder.Resolver) Runtime {
	container.SweepEgressProxies(cfg.EgressNetworkPrefix)
	return &dockerRuntime{
		cfg: cfg, folders: folders, runner: container.DockerRunner{},
		signal: func(name string) error {
			return exec.Command(container.Bin, "kill", "--signal=SIGUSR1", name).Run()
		},
		kill: dockerKill,
	}
}

// dockerKill is the default Kill: stop → docker kill → rm -f, idempotent.
func dockerKill(name string) error {
	_ = exec.Command(container.Bin, container.StopContainerArgs(name)...).Run()
	_ = exec.Command(container.Bin, "kill", name).Run()
	_ = exec.Command(container.Bin, "rm", "-f", name).Run()
	return nil
}

// Run spawns one container turn. routd owns the MCP socket, so runed only
// mounts the ipc dir. Before spawning it registers the steer closure so a
// concurrent POST /v1/runs writes into this live container instead of
// spawning a second (spec 5/P § Steer-into-running-container).
func (d *dockerRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	if spec.RegisterSteer != nil {
		ipcDir, _ := d.folders.IpcPath(spec.Folder)
		spec.RegisterSteer(d.steerInto(ipcDir, spec.ContainerName))
	}
	// armCancel kills the container when ctx dies — the caller (Manager.spawn)
	// wraps ctx with the RunTTL deadline before calling Run, so ctx dying
	// covers both RunTTL elapsing and routd dropping the request. See
	// armCancel's doc comment.
	stopCancel := d.armCancel(ctx, spec.ContainerName)
	defer stopCancel()
	var gc core.GroupConfig
	if len(spec.ContainerConfig) > 0 {
		b, _ := json.Marshal(spec.ContainerConfig)
		_ = json.Unmarshal(b, &gc)
	}
	// container_spawn span + spawn/duration/active metrics (spec 5/O). The span
	// joins the turn trace via ctx (routd injected traceparent over POST /v1/runs).
	folder := string(spec.Folder)
	ctx, endSpan := obs.StartSpan(ctx, "container_spawn",
		"folder", folder, "image", d.cfg.Image)
	obs.ContainerActiveInc()
	start := time.Now()
	out := d.runner.Run(d.cfg, d.folders, container.Input{
		Name:            spec.ContainerName,
		Prompt:          spec.MessageBatch,
		SessionID:       spec.SessionID,
		ChatJID:         spec.ChatJID,
		Channel:         spec.Channel, // drives pickOutputStyle → per-surface formatting
		Folder:          spec.Folder,
		Topic:           spec.Topic,
		MessageID:       spec.TurnID,
		Sender:          spec.TriggerSender,
		Model:           spec.Model,
		Config:          gc,
		Elevated:        spec.Elevated,
		ShareReadOnly:   spec.ShareReadOnly,
		EgressOpen:      spec.Egress,
		WebPublish:      spec.WebPublish,
		GHWrite:         spec.GHWrite,
		Secrets:         spec.Secrets,
		ModelGatewayKey: spec.ModelGatewayKey,
		QueryTimeoutMs:  queryTimeoutMs(spec.RunTTL),
		Egress:          egressConfig(d.cfg, spec.EgressAllowlist),
	})
	obs.ContainerActiveDec()
	outcome := outcomeFor(out)
	var spawnErr error
	if outcome == runedv1.OutcomeError {
		spawnErr = fmt.Errorf("%s", out.Error)
	}
	endSpan(spawnErr)
	obs.RecordContainerSpawn(folder, outcome, time.Since(start).Seconds())
	return RunResult{
		Outcome:      outcome,
		NewSessionID: out.NewSessionID,
		Error:        out.Error,
		ExitCode:     out.ExitCode,
		MessageCount: out.MessageCount,
		Terminal:     out.Terminal,
	}
}

// egressConfig is the one mapping from runed's config to the substrate's egress
// inputs, plus the allowlist routd resolved for this spawn. Both readers — the
// per-spawn proxy and the startup Substrate check — go through it, so they
// cannot disagree on what is set. EgressConfig.Effective decides what it
// delivers: where the image is configured, startEgressProxy runs this spawn's
// proxy and puts the spawn on an --internal network whose only route out is
// that proxy (spec 6/24). Its absence left split spawns on the default
// network, i.e. the open internet.
func egressConfig(cfg *core.Config, allowlist []string) container.EgressConfig {
	return container.EgressConfig{
		NetworkPrefix: cfg.EgressNetworkPrefix,
		ParentSubnet:  cfg.EgressParentSubnet,
		Image:         cfg.EgressImage,
		UplinkNetwork: cfg.EgressUplinkNetwork,
		AllowlistFn:   func(string) ([]string, error) { return allowlist, nil },
		GatewayURL:    cfg.ModelGatewayURL,
	}
}

// Substrate is what this configuration delivers against the Docker
// declaration (container.Profile). main logs it once at start and refuses to
// run on an error: an image named with an input missing would leave every
// spawn unfiltered while the config reads as isolated (spec 6/25 §3).
func Substrate(cfg *core.Config) (substrate.Effective, error) {
	return egressConfig(cfg, nil).Effective()
}

// queryTimeoutMs derives the agent's in-container query timeout from the run
// ceiling: 30s below RunTTL so the agent aborts + delivers a graceful summary
// BEFORE the RunTTL deadline's hard kill (armed by Manager.spawn, see
// armCancel), floored at 60s. RunTTL<=0 (no ceiling) → 0 (agent keeps its
// built-in default).
func queryTimeoutMs(runTTL time.Duration) int64 {
	if runTTL <= 0 {
		return 0
	}
	q := max(runTTL-30*time.Second, 60*time.Second)
	return q.Milliseconds()
}

// deadlineKillPollInterval is how often armCancel retries Kill after ctx
// dies — covers a container that hasn't finished `docker run` (not yet
// killable by name) when ctx.Done() first fires.
const deadlineKillPollInterval = 250 * time.Millisecond

// armCancel kills the container when ctx dies mid-run — whether routd dropped the
// POST /v1/runs request or the run outlived RunTTL. Both are the SAME signal here,
// since Manager.spawn wraps ctx with a RunTTL deadline before calling Run, so this
// Runtime needs no RunTTL-specific code. Without it the container orphans until
// RunTTL and routd re-feeds the turn into a second container.
//
// Once ctx dies the watcher Kills the container by name and keeps retrying on an
// interval, so a slow `docker run` that was not yet killable is still reaped. It
// returns a stop func the caller defers, so no kill fires after the run returns. Kill
// is idempotent, making the retries harmless.
func (d *dockerRuntime) armCancel(ctx context.Context, containerName string) func() {
	if ctx == nil || containerName == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		for {
			_ = d.Kill(containerName)
			select {
			case <-done:
				return
			case <-time.After(deadlineKillPollInterval):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// steerInto returns the steer closure: write the batch as an IPC input file into the
// running container's input dir and SIGUSR1 it. Returns false when the SIGUSR1 fails
// — the container already exited (the documented steer race); the Manager then falls
// through to a fresh spawn and the orphaned IPC file is drained at session start.
func (d *dockerRuntime) steerInto(ipcDir, containerName string) func(batch string) bool {
	return func(batch string) bool {
		if batch == "" || ipcDir == "" || containerName == "" {
			return false
		}
		if err := writeIPCInput(ipcDir, batch); err != nil {
			slog.Error("steerInto: writeIPCInput failed; falling through to a fresh spawn like the documented SIGUSR1 race, but this is a disk-write failure, not a race",
				"container", containerName, "err", err)
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

// outcomeFor maps a container exit to the run outcome. The agent's output goes
// out-of-band over routd's in-process MCP socket, so runed NEVER sees it — the old
// `!HadOutput && Result==""` → Silent test mis-classified EVERY clean run as silent,
// which routd counts as a failure, so the folder's breaker tripped after 3 turns. A
// completed run is OK unless the runner flagged an error or timeout.
func outcomeFor(o container.Output) string {
	if o.Status == "error" || o.Error != "" {
		return runedv1.OutcomeError
	}
	return runedv1.OutcomeOK
}

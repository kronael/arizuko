package runed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/substrate"
)

const (
	// maxOutputSize caps the stderr this runtime keeps for the turn log.
	maxOutputSize = 10 * 1024 * 1024 // 10MB
	// containerGracePeriod is how far above the idle timeout the run ceiling
	// must sit: a deadline below it would kill a container the idle timer had
	// not even given up on (Manager.turnTTL).
	containerGracePeriod = 30 * time.Second
	// containerSoftDeadlineOffset is how long before the deadline the agent is
	// warned to wrap up.
	containerSoftDeadlineOffset = 2 * time.Minute
	// deadlineKillPollInterval is how often armCancel retries Kill after ctx
	// dies, for a container not yet killable by name when ctx.Done() first
	// fires.
	deadlineKillPollInterval = 250 * time.Millisecond
	// agentTerminalExit is sysexits.h EX_CONFIG, which the agent container uses
	// to say the model refused for a reason a retry cannot repair — an
	// exhausted quota or a spend cap, whose reset is hours out (BUGS L175). It
	// is the container's own code, so it must stay clear of the three Docker
	// reserves in terminalExit and of the 128+ signal range.
	agentTerminalExit = 78
)

// execCommand is the process launcher; tests replace it with a helper binary so
// the whole docker half runs without a docker daemon.
var execCommand = exec.Command

// dockerRuntime is the Docker substrate: it delivers one rendered Turn to one
// `docker run --rm` and owns that container's lifecycle — steer-into, the
// deadline kill, teardown. routd hosts the agent MCP socket, so runed only
// mounts the ipc dir.
type dockerRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	// signal SIGUSR1s a running container by name (steer wakeup); tests
	// inject a fake so the steer path runs without docker.
	signal func(name string) error
	// kill stops + removes a container by name; tests inject a fake so the
	// deadline enforcement path runs without docker.
	kill func(name string) error
}

// NewDockerRuntime builds the Docker substrate. It reaps any egress proxies a
// previous runed left behind: an orphan holds a live allowlist, possibly an
// elevated turn's unrestricted one, on a folder network where a later spawn
// could reach it.
func NewDockerRuntime(cfg *core.Config, folders *groupfolder.Resolver) Runtime {
	container.SweepEgressProxies(cfg.EgressNetworkPrefix)
	return &dockerRuntime{
		cfg: cfg, folders: folders,
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

// Run starts one container turn. Before starting it registers the steer
// closure so a concurrent POST /v1/runs writes into this live container
// instead of spawning a second (spec 5/P-runed §"Run state").
func (d *dockerRuntime) Run(ctx context.Context, t Turn) RunResult {
	if t.RegisterSteer != nil {
		ipcDir, _ := d.folders.IpcPath(t.Folder)
		t.RegisterSteer(d.steerInto(ipcDir, t.Handle))
	}
	// Manager.spawn wraps ctx with the turn deadline, so ctx dying covers the
	// ceiling elapsing AND routd dropping the request.
	stopCancel := d.armCancel(ctx, t.Handle)
	defer stopCancel()

	// The span joins the turn trace via ctx (routd injected traceparent over
	// POST /v1/runs). Its own context is dropped, not forgotten: container_spawn
	// is one flat span with no children to parent.
	_, endSpan := obs.StartSpan(ctx, "container_spawn",
		"folder", t.Folder, "image", d.cfg.Image)
	obs.ContainerActiveInc()
	start := time.Now()

	res := d.spawn(ctx, t)

	obs.ContainerActiveDec()
	var spawnErr error
	if res.Outcome == runedv1.OutcomeError {
		spawnErr = fmt.Errorf("%s", res.Error)
	}
	endSpan(spawnErr)
	obs.RecordContainerSpawn(t.Folder, res.Outcome, time.Since(start).Seconds())
	return res
}

// spawn is the docker half: the proxy, the argument list, the process, its
// stderr and its timers. Every exit returns the turn's RunResult, so Run's
// span and metrics have exactly one place to close.
func (d *dockerRuntime) spawn(ctx context.Context, t Turn) RunResult {
	// This turn's own egress proxy, started and proved healthy BEFORE the agent
	// container exists. A failure here fails the whole turn: starting the agent
	// anyway would put an unfiltered container on the network, the exact
	// inversion of the property egress exists for.
	eg, eerr := container.StartEgressProxy(egressConfig(d.cfg, t.Egress), t.Folder, t.Handle)
	if eerr != nil {
		return RunResult{Outcome: runedv1.OutcomeError, Error: "egress proxy: " + eerr.Error()}
	}
	defer eg.Stop()

	args := buildArgs(d.cfg, t, eg)

	groupDir, _ := d.folders.GroupPath(t.Folder)
	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)

	slog.Info("spawning container",
		"group", t.Folder, "container", t.Handle, "mounts", len(t.Mounts))
	slog.Debug("container args", "group", t.Folder, "args", strings.Join(args, " "))
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryAgent,
		Action:   "container.spawn",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "containers/" + t.Handle,
		Folder:   t.Folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"image":    d.cfg.Image,
			"elevated": t.Env["ARIZUKO_IS_ROOT"] == "1",
		},
	})

	start := time.Now()

	cmd := execCommand(container.Bin, args...)
	cmd.Stdout = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return RunResult{Outcome: runedv1.OutcomeError, Error: "stdin pipe: " + err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{Outcome: runedv1.OutcomeError, Error: "stderr pipe: " + err.Error()}
	}
	if err := cmd.Start(); err != nil {
		// The docker CLI did not launch (missing or not executable) — a
		// host misconfiguration; a retry spawns nothing (BUGS F73).
		return RunResult{
			Outcome: runedv1.OutcomeError,
			Error:   "start: " + err.Error(), Terminal: true,
		}
	}

	if _, err := stdin.Write(t.Stdin); err != nil {
		slog.Error("stdin write failed", "group", t.Folder, "err", err)
	}
	stdin.Close()

	var timedOut atomic.Bool
	var stopOnce sync.Once
	stopContainer := func(reason string) {
		stopOnce.Do(func() {
			timedOut.Store(true)
			slog.Info("container stopping",
				"reason", reason, "group", t.Folder, "container", t.Handle)
			stop := exec.Command(container.Bin, container.StopContainerArgs(t.Handle)...)
			if err := stop.Run(); err != nil {
				slog.Warn("graceful stop failed, killing container",
					"group", t.Folder, "container", t.Handle, "err", err)
				// docker kill, not cmd.Process.Kill() — the latter only kills the CLI client.
				if kerr := exec.Command(container.Bin, "kill", t.Handle).Run(); kerr != nil {
					slog.Warn("docker kill failed, forcing removal",
						"group", t.Folder, "container", t.Handle, "err", kerr)
					exec.Command(container.Bin, "rm", "-f", t.Handle).Run()
				}
			}
		})
	}

	// Idle timer + resetIdle are created before the stderr goroutine that
	// reads resetIdle — the var is written exactly once, prior to any
	// concurrent read, so no synchronization on the func value is needed.
	idleTimer := time.AfterFunc(d.cfg.IdleTimeout, func() {
		stopContainer("idle timeout")
	})
	defer idleTimer.Stop()
	resetIdle := func() { idleTimer.Reset(d.cfg.IdleTimeout) }

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	// Declared early so the stderr goroutine can reach it.
	var idleResets atomic.Int32
	const maxIdleResets = 240 // 60s * 240 = 4h max via idle resets
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), maxOutputSize)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "[ant]") {
				slog.Info("container agent", "group", t.Folder, "line", line)
				if idleResets.Add(1) <= maxIdleResets {
					resetIdle()
				}
			} else {
				slog.Debug("container stderr", "group", t.Folder, "line", line)
			}
			stderrMu.Lock()
			if stderrBuf.Len() < maxOutputSize {
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
			}
			stderrMu.Unlock()
		}
	}()

	// The turn's one deadline is ctx's (spec 6/41 §"The seam"): the run ceiling
	// with the group's container_config Timeout folded in. This substrate's own
	// timers read it rather than holding a second ceiling of their own.
	ceiling := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		ceiling = time.Until(dl)
	}
	if ceiling > 0 {
		deadline := time.AfterFunc(ceiling, func() { stopContainer("hard deadline") })
		defer deadline.Stop()
		if ceiling > containerSoftDeadlineOffset {
			soft := time.AfterFunc(ceiling-containerSoftDeadlineOffset, func() {
				if timedOut.Load() {
					return
				}
				slog.Info("soft deadline firing, warning agent",
					"group", t.Folder, "container", t.Handle)
				d.warnWrapUp(t.Folder, t.Handle)
			})
			defer soft.Stop()
		}
	}

	exitErr := cmd.Wait()
	elapsed := time.Since(start)

	code := 0
	if exitErr != nil {
		var ee *exec.ExitError
		if errors.As(exitErr, &ee) {
			code = ee.ExitCode()
		}
	}

	stderrMu.Lock()
	stderrStr := stderrBuf.String()
	stderrMu.Unlock()

	ts := time.Now().Format("2006-01-02T15-04-05")
	logFile := filepath.Join(logsDir, "container-"+ts+".log")
	to := timedOut.Load()
	writeLog(logFile, t, elapsed, code, to, stderrStr)

	slog.Info("container exited",
		"group", t.Folder, "container", t.Handle, "code", code,
		"duration", elapsed, "timedOut", to)
	exitOutcome := audit.OutcomeOK
	if code != 0 || to {
		exitOutcome = audit.OutcomeError
	}
	exitErrMsg := ""
	if to {
		exitErrMsg = "timeout"
	} else if code != 0 {
		exitErrMsg = fmt.Sprintf("exit_code=%d", code)
	}
	audit.Emit(context.Background(), audit.Event{
		Category:   audit.CategoryAgent,
		Action:     "container.exit",
		Actor:      "system",
		Surface:    audit.SurfaceGateway,
		Resource:   "containers/" + t.Handle,
		Folder:     t.Folder,
		Outcome:    exitOutcome,
		ErrorMsg:   exitErrMsg,
		DurationMS: elapsed.Milliseconds(),
		ParamsSummary: map[string]any{
			"exit_code": code,
			"timed_out": to,
		},
	})

	msgCount := int(idleResets.Load()) // [ant] lines observed this spawn

	if to {
		slog.Error("container timed out",
			"group", t.Folder, "container", t.Handle, "duration", elapsed)
		return RunResult{
			Outcome:      runedv1.OutcomeError,
			Error:        fmt.Sprintf("Container timed out after %s", elapsed.Round(time.Second)),
			ExitCode:     code,
			MessageCount: msgCount,
		}
	}

	if code != 0 {
		slog.Error("container exited with error",
			"group", t.Folder, "container", t.Handle, "code", code,
			"duration", elapsed, "logFile", logFile)
		tail := stderrStr
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return RunResult{
			Outcome:      runedv1.OutcomeError,
			Error:        fmt.Sprintf("Container exited with code %d: %s", code, tail),
			ExitCode:     code,
			MessageCount: msgCount,
			Terminal:     terminalExit(code),
		}
	}

	// The agent's output goes out-of-band over routd's MCP socket, so this
	// substrate NEVER sees it: a completed run is OK. Classifying on missing
	// output tripped every folder's breaker after 3 clean turns.
	return RunResult{Outcome: runedv1.OutcomeOK, MessageCount: msgCount}
}

// warnWrapUp tells the agent its deadline is close: an IPC message it drains
// mid-turn, plus the SIGUSR1 that wakes it to read.
func (d *dockerRuntime) warnWrapUp(folder, handle string) {
	ipcDir, _ := d.folders.IpcPath(folder)
	if ipcDir != "" {
		inputDir := groupfolder.IpcInputDir(ipcDir)
		os.MkdirAll(inputDir, 0o755)
		name := fmt.Sprintf("%d-deadline.json", time.Now().UnixMilli())
		fp := filepath.Join(inputDir, name)
		tmp := fp + ".tmp"
		payload, _ := json.Marshal(map[string]string{
			"type": "message",
			"text": "⚠️ SYSTEM: You have ~2 minutes before this session is forcefully terminated. Wrap up NOW: summarize what you accomplished, what is still pending, and deliver your response to the user.",
		})
		if err := os.WriteFile(tmp, payload, 0o644); err == nil {
			if err := os.Rename(tmp, fp); err != nil {
				os.Remove(tmp)
			}
		}
	}
	exec.Command(container.Bin, "kill", "--signal=SIGUSR1", handle).Run()
}

// buildArgs renders the Turn as a `docker run` argument list. The env map is
// emitted in key order so one turn always renders one argument list.
func buildArgs(cfg *core.Config, t Turn, eg container.EgressSpawn) []string {
	args := []string{
		"run", "-i", "--rm",
		"--name", t.Handle,
		"--shm-size=1g",
	}
	env := map[string]string{}
	maps.Copy(env, t.Env)
	// The network is --internal, so this is the whole egress story: the only
	// route out is eg.ProxyURL, and that proxy answers eg.IP and no other. The
	// proxy variables are this substrate's own address, which is why it adds
	// them and the renderer does not.
	if eg.Network != "" {
		args = append(args, "--network", eg.Network, "--ip", eg.IP)
		env["HTTP_PROXY"] = eg.ProxyURL
		env["HTTPS_PROXY"] = eg.ProxyURL
		env["NO_PROXY"] = "localhost,127.0.0.1"
		env["NODE_OPTIONS"] = "--require=/app/proxy-shim.js"
	}
	// The uid the agent runs as inside the container, and HOME with it: both are
	// this substrate's mapping of the host uid, not a fact of the turn.
	if uid, explicit := container.RunUID(); explicit {
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, os.Getgid()))
		env["HOME"] = core.ContainerHome
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, "-e", k+"="+env[k])
	}
	for _, m := range t.Mounts {
		spec := m.Host + ":" + m.Container
		if m.RO {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	return append(args, cfg.Image)
}

// terminalExit reports whether the exit code proves a configuration
// fault. Docker reserves 125 (docker run failed — bad image or bad
// flags), 126 (command not runnable), and 127 (command not found) for
// invocation failures; a retry cannot repair them. Every other code
// stays retryable (BUGS F73, spec 5/12).
func terminalExit(code int) bool {
	return code == 125 || code == 126 || code == 127 || code == agentTerminalExit
}

// egressConfig is the one mapping from runed's config to the substrate's
// egress inputs, plus the allowlist the renderer resolved for this turn. Both
// readers — the per-turn proxy and the startup Substrate check — go through
// it. Where the image is configured, the spawn runs on an --internal network
// whose only route out is its proxy (spec 6/24).
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
// declaration. main refuses to run on an error: an image named with an input
// missing would leave every spawn unfiltered while the config reads as
// isolated (spec 6/25 §3).
func Substrate(cfg *core.Config) (substrate.Effective, error) {
	return egressConfig(cfg, nil).Effective()
}

// armCancel kills the container when ctx dies mid-run, whether routd dropped
// the request or the run outlived its ceiling; Manager.spawn wraps ctx with the
// deadline, so this runtime needs no ceiling-specific code. Without it the
// container orphans and routd re-feeds the turn into a second container. The
// watcher retries Kill on an interval so a slow `docker run` is still reaped;
// the returned stop func ends it when the run returns.
func (d *dockerRuntime) armCancel(ctx context.Context, handle string) func() {
	if ctx == nil || handle == "" {
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
			_ = d.Kill(handle)
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

// steerInto returns the steer closure: write the batch as an IPC input file
// and SIGUSR1 the container. Returns false when the SIGUSR1 fails (the
// container already exited); the Manager then falls through to a fresh spawn
// and the orphaned IPC file is drained at session start.
func (d *dockerRuntime) steerInto(ipcDir, handle string) func(batch string) bool {
	return func(batch string) bool {
		if batch == "" || ipcDir == "" || handle == "" {
			return false
		}
		if err := writeIPCInput(ipcDir, batch); err != nil {
			slog.Error("steerInto: writeIPCInput failed; falling through to a fresh spawn like the documented SIGUSR1 race, but this is a disk-write failure, not a race",
				"container", handle, "err", err)
			return false
		}
		return d.signal(handle) == nil
	}
}

// Kill stops a live container by name (spec 5/P-runed § DELETE /v1/runs/{id}).
// Idempotent.
func (d *dockerRuntime) Kill(handle string) error {
	if handle == "" {
		return nil
	}
	if d.kill == nil {
		return dockerKill(handle)
	}
	return d.kill(handle)
}

// writeIPCInput drops one {type:"message",text} file into the container's IPC
// input dir via temp+rename, so the agent's drainIpcInput never sees a partial
// file.
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

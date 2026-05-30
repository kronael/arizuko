package runed

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
	"github.com/kronael/arizuko/ipc"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// FakeRuntime backs the contract test + standalone acceptance: it invokes a
// caller-supplied function with the RunSpec (which can drive the federated
// callbacks against routd) and returns its outcome. No docker, no socket
// (spec 5/P § acceptance: FakeRuntime backs unit tests of the envelope
// without spawning anything).
type FakeRuntime struct {
	Fn func(ctx context.Context, spec RunSpec) RunResult
}

// Run invokes the injected function.
func (f FakeRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	if f.Fn == nil {
		return RunResult{Outcome: runedv1.OutcomeSilent}
	}
	return f.Fn(ctx, spec)
}

// Kill is a no-op for the fake (no container to stop).
func (FakeRuntime) Kill(string) error { return nil }

// dockerRuntime is the production Runtime: it stands up the per-tenant MCP
// host (ipc.ServeMCP) with GatedFns repointed at HTTP forwards into routd
// (the Federator), then spawns the per-turn container via container.Run.
// The envelope (socket, spawn, stream, teardown) is owned here; frames
// arrive out-of-band via the federated callbacks. runed never appends.
type dockerRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	runner  container.Runner
	fed     *Federator
	// db is runed's own store, read directly for the runed-local StoreFns
	// (RecentSessions ← session_log). nil disables those tools (local-dev).
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

// NewDockerRuntime builds the production Runtime around the docker runner +
// the federation forward to routd. fed forwards the agent's message tools
// to routd /v1/turns/{turn_id}/* (the sole appender); db backs the
// runed-local read StoreFns (session_log).
func NewDockerRuntime(cfg *core.Config, folders *groupfolder.Resolver, fed *Federator, db *DB) Runtime {
	return &dockerRuntime{
		cfg: cfg, folders: folders, runner: container.DockerRunner{}, fed: fed, db: db,
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

// Run spawns one container turn. GatedFns are repointed at the Federator so
// the agent's reply/send/like/... tool calls forward to routd over HTTP,
// stamped with this run's turn_id + the brokered token. submit_turn fans to
// routd's /result twin. Before spawning it registers the steer closure so a
// concurrent POST /v1/runs writes into this live container instead of
// spawning a second (spec 5/P § Steer-into-running-container).
func (d *dockerRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	if spec.RegisterSteer != nil {
		ipcDir, _ := d.folders.IpcPath(spec.Folder)
		spec.RegisterSteer(d.steerInto(ipcDir, spec.ContainerName))
	}
	stopTTL := d.armRunTTL(spec.RunTTL, spec.ContainerName)
	defer stopTTL()
	gated := d.gatedFns(ctx, spec)
	store := d.storeFns(ctx, spec)
	out := d.runner.Run(d.cfg, d.folders, container.Input{
		Name:      spec.ContainerName,
		Prompt:    spec.MessageBatch,
		SessionID: spec.SessionID,
		ChatJID:   spec.ChatJID,
		Folder:    spec.Folder,
		Topic:     spec.Topic,
		MessageID: spec.TurnID,
		Sender:    spec.TriggerSender,
		GatedFns:  gated,
		StoreFns:  store,
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

// gatedFns builds the federation forward: every message tool the agent
// calls is HTTP-forwarded to routd, stamped with spec.TurnID + spec.Token.
// idemKey is per-call; the agent's tool layer is at-least-once, so a stable
// per-call key keeps the routd ledger honest.
func (d *dockerRuntime) gatedFns(ctx context.Context, spec RunSpec) ipc.GatedFns {
	idem := func() string { return "fed-" + randHex(8) }
	return ipc.GatedFns{
		SendReply: func(jid, text, replyTo string) (string, error) {
			r, err := d.fed.Forward(ctx, "reply", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "text": text, "reply_to_id": replyTo})
			return platformID(r), err
		},
		SendMessage: func(jid, text string) (string, error) {
			r, err := d.fed.Forward(ctx, "send", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "text": text})
			return platformID(r), err
		},
		SendDocument: func(jid, path, name, caption, replyTo, _ string) error {
			_, err := d.fed.Forward(ctx, "send_file", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "path": path, "name": name, "caption": caption, "reply_to_id": replyTo})
			return err
		},
		Like: func(jid, target, reaction string) error {
			_, err := d.fed.Forward(ctx, "like", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target, "reaction": reaction})
			return err
		},
		Dislike: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "dislike", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Edit: func(jid, target, content string) error {
			_, err := d.fed.Forward(ctx, "edit", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target, "content": content})
			return err
		},
		Delete: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "delete", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Pin: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "pin_message", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Unpin: func(jid, target string, all bool) error {
			tool := "unpin_message"
			if all {
				tool = "unpin_all"
			}
			_, err := d.fed.Forward(ctx, tool, spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		SubmitTurn: func(folder string, t ipc.TurnResult) error {
			_, err := d.fed.Result(ctx, spec.TurnID, spec.Token, "turn-"+spec.TurnID, routdv1.TurnResult{
				TurnID: spec.TurnID, SessionID: t.SessionID, Status: t.Status,
				Result: t.Result, Error: t.Error,
				CallerSub: t.CallerSub, Models: modelCosts(t.Models),
			})
			return err
		},
	}
}

func outcomeFor(o container.Output) string {
	switch {
	case o.Status == "error":
		return runedv1.OutcomeError
	case !o.HadOutput && o.Result == "":
		return runedv1.OutcomeSilent
	default:
		return runedv1.OutcomeOK
	}
}

func platformID(r any) string {
	if sr, ok := r.(routdv1.SendResult); ok {
		return sr.PlatformID
	}
	return ""
}

// modelCosts maps the agent's per-model usage (ipc.ModelUsage) onto routd's
// cost-log shape (routdv1.ModelCost). routd persists Input/Output/CostCents;
// the agent's CacheRead/CacheWrite fold into CostCents upstream and are not a
// cost_log column. nil/empty in → nil out (the cost-less ant path).
func modelCosts(in map[string]ipc.ModelUsage) map[string]routdv1.ModelCost {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]routdv1.ModelCost, len(in))
	for model, u := range in {
		out[model] = routdv1.ModelCost{Input: u.Input, Output: u.Output, CostCents: u.CostCents}
	}
	return out
}

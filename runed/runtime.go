package runed

import (
	"context"
	"errors"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

const (
	// maxOutputSize caps the stderr a substrate keeps for the turn log.
	maxOutputSize = 10 * 1024 * 1024 // 10MB
	// agentTerminalExit is sysexits.h EX_CONFIG, which the agent uses to say the
	// model refused for a reason a retry cannot repair — an exhausted quota or a
	// spend cap, whose reset is hours out (BUGS L175). Guest-init reports it
	// for a handler that is invalid, missing or not executable. It stays clear
	// of the 128+ signal range.
	agentTerminalExit = 78
)

// Runtime starts one guest and stops it. ctx carries the deadline.
//
// There is one production implementation, the KVM guest of spec 6/41
// (NewQemuRuntime in qemu.go). holdRuntime is the executor for a run that starts
// nothing, and FakeRuntime backs tests without a hypervisor. The port is two methods: a
// third, or a Turn field a substrate must interpret rather than deliver, needs
// an approved plan first (root CLAUDE.md, "A port is two methods and stays
// two"; spec 6/41 §"The seam").
type Runtime interface {
	Run(ctx context.Context, t Turn) RunResult
	// Kill stops a live guest by handle (DELETE /v1/runs/{id}). A no-op for a
	// runtime with nothing running.
	Kill(handle string) error
}

// Turn is the envelope: one guest, rendered. A substrate delivers every field
// and interprets none. renderTurn is the one producer (spec 6/41
// §"The renderer"); the substrates and the fake are the only consumers.
type Turn struct {
	// Handle is what the substrate starts, steers and kills by.
	Handle string
	// Folder is the one path the substrate resolves: the ipc dir of the steer,
	// the logs/ dir of the turn log, the name of the egress network.
	Folder string
	// Mounts is the mount table's right-hand side; Container is the guest path.
	Mounts []core.Mount
	// Env is the agent's process environment. A substrate adds the proxy
	// variables (HTTP_PROXY and its five companions) because their value is
	// its own address.
	Env map[string]string
	// Stdin is the agent payload: ant/src/index.ts ContainerInput.
	Stdin []byte
	// Egress is the proxy allowlist, grants applied.
	Egress []string
	// RegisterSteer is the Manager's hook the substrate calls ONCE the guest
	// and its ipc dir are up. The callback returns false after the guest exits;
	// fresh Run requests are queued by routd and do not invoke it.
	RegisterSteer func(steer func(batch string) bool)
}

// RunResult is the harness outcome at the turn boundary. Outcome ∈ ok|error|silent.
type RunResult struct {
	Outcome      string // ok|error|silent
	Error        string
	ExitCode     int
	MessageCount int
	// Terminal forbids automatic retry, including after a run timeout.
	Terminal bool
}

// runFailure marks a preparation or control failure terminal only for an ended
// context or an explicit permanent fact. An unclassified error
// is retryable and the turn budget bounds the retries (BUGS L887).
func runFailure(ctx context.Context, err error) RunResult {
	var fact *retry.Error
	permanent := errors.As(err, &fact) && fact.Failure.Kind == "permanent"
	return RunResult{Outcome: runedv1.OutcomeError, Error: err.Error(),
		Terminal: ctx.Err() != nil || permanent}
}

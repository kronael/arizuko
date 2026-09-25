package runed

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
)

// The turn's boundaries, read by whatever substrate runs it. These four are the
// turn's contract with the Manager and the agent, not one substrate's: the run
// ceiling against the idle timer, the wrap-up warning, the code the agent uses
// to say a retry cannot help, and the cap on the stderr the turn log keeps. They
// lived in the Docker runtime until spec 6/41 step 4 deleted it.
const (
	// maxOutputSize caps the stderr a substrate keeps for the turn log.
	maxOutputSize = 10 * 1024 * 1024 // 10MB
	// containerGracePeriod is how far above the idle timeout the run ceiling
	// must sit: a deadline below it would kill a guest the idle timer had not
	// even given up on (Manager.turnTTL).
	containerGracePeriod = 30 * time.Second
	// containerSoftDeadlineOffset is how long before the deadline the agent is
	// warned to wrap up.
	containerSoftDeadlineOffset = 2 * time.Minute
	// agentTerminalExit is sysexits.h EX_CONFIG, which the agent uses to say the
	// model refused for a reason a retry cannot repair — an exhausted quota or a
	// spend cap, whose reset is hours out (BUGS L175). It is the agent's own
	// code, so it stays clear of the 128+ signal range.
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
	// variables (HTTP_PROXY and its three companions) because their value is
	// its own address.
	Env map[string]string
	// Stdin is the agent payload: ant/src/index.ts ContainerInput.
	Stdin []byte
	// Egress is the proxy allowlist, grants applied.
	Egress []string
	// RegisterSteer is the Manager's hook the substrate calls ONCE the guest
	// and its ipc dir are up, handing back a steer closure so a concurrent
	// POST /v1/runs steers into this live guest (spec 5/P-runed). The closure
	// returns false when the guest has already exited.
	RegisterSteer func(steer func(batch string) bool)
}

// RunResult is the harness outcome at the turn boundary. NewSessionID is the
// session id the harness ran/produced. Outcome ∈ ok|error|silent.
type RunResult struct {
	Outcome      string // ok|error|silent
	NewSessionID string
	Error        string
	ExitCode     int
	MessageCount int
	// Terminal reports a failure that a retry cannot repair (the
	// guest invocation itself failed). The zero value means
	// retryable (spec 5/12).
	Terminal bool
}

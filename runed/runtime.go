package runed

import (
	"context"

	"github.com/kronael/arizuko/core"
)

// Runtime starts one guest and stops it. ctx carries the deadline.
//
// One production implementation is chosen at startup (NewDockerRuntime in
// docker.go), holdRuntime is the executor for a run that starts nothing, and
// FakeRuntime backs tests without a substrate. The port is two methods: a
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

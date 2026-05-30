package runed

import (
	"context"
	"time"

	"github.com/kronael/arizuko/types"
)

// RunSpec is one agent invocation as runed sees it: the rendered prompt,
// the resume session id (empty = fresh), the brokered capability token the
// agent uses to call back into routd, and the turn_id stamped onto every
// callback. Mirrors the PINNED POST /v1/runs body (spec 5/P).
type RunSpec struct {
	RunID         string
	Folder        string
	ContainerName string // the Manager's pinned name; the Runtime spawns + Kills by it
	Topic         string
	ChatJID       string
	SessionID     string // resume; empty = fresh
	MessageBatch  string // rendered prompt STRING
	TriggerSender string
	CallerSub     types.UserSub
	TurnID        string
	Token         string // brokered capability token (the JWS, in memory only)
	Isolated      bool

	// RegisterSteer is the Manager's hook the Runtime calls ONCE the
	// container + IPC dir are up, handing back a steer closure (IPC write +
	// SIGUSR1) so a concurrent POST /v1/runs steers into this live spawn
	// (spec 5/P § Steer-into-running-container). The closure returns false
	// when the container has already exited (the documented steer race).
	RegisterSteer func(steer func(batch string) bool)
}

// RunResult is the harness outcome at the turn boundary. NewSessionID is
// the session id the harness ran/produced (echoed onto spawns + the
// POST /v1/runs backstop). Outcome ∈ ok|error|silent.
type RunResult struct {
	Outcome      string // ok|error|silent
	NewSessionID string
	Error        string
	ExitCode     int
	MessageCount int
}

// Runtime executes one agent turn end to end (the execution-session
// envelope, spec 5/P § envelope). It is the ContainerRuntime seam:
// production wraps container.DockerRunner + the ipc MCP host;
// LocalRuntime/FakeRuntime back CI + unit tests without docker. The
// envelope (socket, token, spawn, stream, teardown) is owned by Runtime;
// frames arrive out-of-band via the agent's callbacks into routd.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) RunResult
	// Kill stops a live container by name (DELETE /v1/runs/{id}): stop,
	// then docker kill, then rm -f (spec 5/P § DELETE). A no-op for runtimes
	// with no container (FakeRuntime/LocalRuntime return nil).
	Kill(containerName string) error
}

// Broker brokers a downscoped capability token per spawn (spec 5/P §
// brokering). runed mints nothing — production calls authd's downscope
// endpoint; the test stub returns a fixed token. The returned jti is what
// runed persists into mcp_tokens; the raw JWS lives only in the agent's
// process memory (delivered over the SO_PEERCRED-gated MCP socket).
type Broker interface {
	// Broker downscopes the runed service token to sub+folder with
	// scope ⊆ runed.scope ∩ want, ttl ≤ run deadline. Returns the JWS,
	// its jti, and expiry.
	Broker(ctx context.Context, sub types.UserSub, folder string, want []types.Scope, ttl time.Duration) (jws, jti, expiresAt string, err error)
}

// staticBroker returns one fixed token for every spawn — the test/standalone
// stub (a "fake AUTHD_URL returning a fixed downscoped token", spec 5/P §
// acceptance). Never used in production.
type staticBroker struct {
	jws, jti string
}

func (b staticBroker) Broker(_ context.Context, _ types.UserSub, _ string, _ []types.Scope, _ time.Duration) (string, string, string, error) {
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return b.jws, b.jti, exp, nil
}

// NewStaticBroker builds a Broker that always returns (jws, jti). For CI /
// standalone acceptance only.
func NewStaticBroker(jws, jti string) Broker { return staticBroker{jws: jws, jti: jti} }

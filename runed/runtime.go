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
	RunID           string
	Folder          string
	ContainerName   string // the Manager's pinned name; the Runtime spawns + Kills by it
	Topic           string
	ChatJID         string
	Channel         string // JID scheme → container.Input.Channel → pickOutputStyle
	SessionID       string // resume; empty = fresh
	MessageBatch    string // rendered prompt STRING
	TriggerSender   string
	CallerSub       types.UserSub
	TurnID          string
	Isolated        bool
	Elevated        bool              // operator /root elevation → container.Input.Elevated (tier 0 + all-groups mount)
	Model           string            // per-group model override; empty = instance default
	ContainerConfig map[string]any    // opaque GroupConfig forwarded from groups.container_config
	ShareReadOnly   bool              // 5/33 grant decision → container.Input.ShareReadOnly (RO share mount)
	Egress          bool              // 5/33 grant decision → container.Input.Egress (append "*" to allowlist)
	WebPublish      bool              // 5/33 grant decision → container.Input.WebPublish (mount web surfaces)
	EgressAllowlist []string          // routd-resolved crackbox allowlist → EgressConfig.AllowlistFn
	Secrets         map[string]string // routd-resolved folder+user secrets (BYOA) → container.Input.Secrets, injected as container env

	// RunTTL is the run ceiling (the brokered token's TTL). The Runtime
	// enforces it as a kill-deadline FROM WITHIN the run path so the kill is
	// armed only once the run is underway and stops deterministically when the
	// run returns — no detached manager timer racing container creation
	// (spec 5/P § The queue + container model). Zero = no ceiling.
	RunTTL time.Duration

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

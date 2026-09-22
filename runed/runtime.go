package runed

import (
	"context"
	"time"

	"github.com/kronael/arizuko/types"
)

// RunSpec is one agent invocation as runed sees it: the rendered prompt, the
// resume session id (empty = fresh) and the turn_id stamped onto every
// callback. Mirrors the PINNED POST /v1/runs body (spec 5/P-runed).
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
	Kind            string // 'agent' | 'backup' | ... — selects the Executor (spec 5/8)
	Isolated        bool
	Elevated        bool              // operator /root elevation → container.Input.Elevated (tier 0 + all-groups mount)
	Model           string            // per-group model override; empty = instance default
	Effort          string            // reasoning depth for the harness CLI; empty = its own default
	ContainerConfig map[string]any    // opaque GroupConfig forwarded from groups.container_config
	ShareReadOnly   bool              // 5/33-paths-roles grant decision → container.Input.ShareReadOnly (RO share mount)
	Egress          bool              // 5/33-paths-roles grant decision → container.Input.Egress (append "*" to allowlist)
	WebPublish      bool              // 5/33-paths-roles grant decision → container.Input.WebPublish (mount web surfaces)
	GHWrite         bool              // 5/33-paths-roles grant decision → container.Input.GHWrite (ant's gh gate)
	EgressAllowlist []string          // routd-resolved egress allowlist → EgressConfig.AllowlistFn
	Secrets         map[string]string // routd-resolved user env-profile keys (BYOA model creds) → container.Input.Secrets, injected as container env; capability creds are brokered, never here
	// ModelGatewayKey is this turn's virtual key at the model gateway, minted
	// by routd (spec 6/23). Empty means no gateway on this instance.
	ModelGatewayKey string

	// RunTTL is the run ceiling. The Runtime enforces it as a kill-deadline
	// FROM WITHIN the run path, so the kill is armed only once the run is
	// underway and stops when the run returns (spec 5/P-runed). Zero = no ceiling.
	RunTTL time.Duration

	// RegisterSteer is the Manager's hook the Runtime calls ONCE the
	// container + IPC dir are up, handing back a steer closure so a
	// concurrent POST /v1/runs steers into this live spawn (spec 5/P-runed). The
	// closure returns false when the container has already exited.
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
	// container invocation itself failed). The zero value means
	// retryable (spec 5/12).
	Terminal bool
}

// Runtime executes one agent turn end to end (spec 5/P-runed § envelope):
// production wraps container.DockerRunner; FakeRuntime backs tests without
// docker. Frames arrive out-of-band via the agent's callbacks into routd.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) RunResult
	// Kill stops a live container by name (DELETE /v1/runs/{id}). A no-op for
	// runtimes with no container.
	Kill(containerName string) error
}

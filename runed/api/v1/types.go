// Package v1 is runed's published contract: the wire types + a thin HTTP
// client for POST /v1/runs and the rest of the /v1/* surface. It imports
// only types/ — no arizuko-internal domain packages — so routd can call
// runed without dragging in core (spec 5/U § Per-service api/v1).
//
// The POST /v1/runs request/response shapes are PINNED, identical to the
// peer rendering in specs/5/E-routd.md § The routd↔runed interface and
// specs/5/P-runed.md § The routd↔runed interface. Any drift between this
// package and routd's call site is a contract break.
package v1

import "github.com/kronael/arizuko/types"

// RunRequest is the body of POST /v1/runs: routd decided a batch routes
// to Folder and rendered the prompt; runed runs it. message_batch is the
// rendered prompt STRING (sysMsgs+autocalls+persona+<observed>+feed), not
// an array — routd renders, runed runs.
type RunRequest struct {
	Folder           types.Folder   `json:"folder"`
	Topic            string         `json:"topic"`
	ChatJID          string         `json:"chat_jid"`
	SessionID        string         `json:"session_id"`     // empty = fresh; runed resumes if non-empty
	MessageBatch     string         `json:"message_batch"`  // rendered prompt STRING
	TriggerSender    string         `json:"trigger_sender"` // engagement-skip policy only; NOT token identity
	CallerSub        types.UserSub  `json:"caller_sub"`     // token SUBJECT for the brokered agent token; never ""
	TurnID           string         `json:"turn_id"`        // triggering inbound id; echoed on callbacks
	CapabilityScopes []types.Scope  `json:"capability_scopes"`
	Model            string         `json:"model"`            // group override; empty = instance default
	ContainerConfig  map[string]any `json:"container_config"` // opaque GroupConfig forwarded from groups.container_config
	Isolated         bool           `json:"isolated"`         // timed-isolated:* runs: one-off container, no session persist
	// Grants is the per-folder grant ruleset routd derived (tier defaults + ACL);
	// runed sets it on container.Input so buildMounts (share_mount) + the tier-0/1
	// egress "*" logic see it. EgressAllowlist is the resolved crackbox allowlist
	// (network_rules ancestry); runed wires it into the EgressConfig.AllowlistFn so
	// the spawn is attached to the egress-isolated network. Both empty = no
	// constraint (runed has neither store; routd is the authz plane — spec 5/E).
	Grants          []string `json:"grants,omitempty"`
	EgressAllowlist []string `json:"egress_allowlist,omitempty"`
}

// RunOutcome is the synchronous response of POST /v1/runs, returned when
// the run completes (the turn boundary). Frames arrive out-of-band during
// the run via the /v1/turns/{turn_id}/* callbacks, not here.
//
// Outcome ∈ ok|error|silent. Steered=true is the discriminator for a
// steer ack (the call returned immediately because the folder already had
// a live spawn); routd then does NOT advance the cursor. BreakerOpen=true
// rides only on the run that trips the circuit breaker.
type RunOutcome struct {
	RunID       string `json:"run_id"`
	Outcome     string `json:"outcome"` // ok|error|silent
	SessionID   string `json:"session_id"`
	Error       string `json:"error"`
	Steered     bool   `json:"steered"`
	BreakerOpen bool   `json:"breaker_open"`
}

// Outcome values (the contract routd keys on).
const (
	OutcomeOK     = "ok"
	OutcomeError  = "error"
	OutcomeSilent = "silent"
)

// RunStatus is GET /v1/runs/{run_id}. session_id is the runtime echo read
// from spawns.session_id (envelope step 4) — runed never consults routd's
// lineage-authoritative sessions for this.
type RunStatus struct {
	RunID     string `json:"run_id"`
	Folder    string `json:"folder"`
	Topic     string `json:"topic"`
	State     string `json:"state"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"session_id"`
	Steered   bool   `json:"steered"`
	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

// SessionRow is one GET /v1/sessions entry (session_log, dashd run
// history).
type SessionRow struct {
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at"`
	Result       string `json:"result"`
	MessageCount int    `json:"message_count"`
}

// SessionsResponse is GET /v1/sessions.
type SessionsResponse struct {
	Sessions []SessionRow `json:"sessions"`
}

// Err is the uniform JSON error envelope across the /v1/* surface.
type Err struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

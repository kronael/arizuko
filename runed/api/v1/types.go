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
	Channel          string         `json:"channel,omitempty"` // JID scheme (telegram|slack|discord|web); drives the per-surface output style. Empty = default.
	SessionID        string         `json:"session_id"`        // empty = fresh; runed resumes if non-empty
	MessageBatch     string         `json:"message_batch"`     // rendered prompt STRING
	TriggerSender    string         `json:"trigger_sender"`    // engagement-skip policy only; NOT token identity
	CallerSub        types.UserSub  `json:"caller_sub"`        // token SUBJECT for the brokered agent token; never ""
	TurnID           string         `json:"turn_id"`           // triggering inbound id; echoed on callbacks
	CapabilityScopes []types.Scope  `json:"capability_scopes"`
	Model            string         `json:"model"`            // group override; empty = instance default
	ContainerConfig  map[string]any `json:"container_config"` // opaque GroupConfig forwarded from groups.container_config
	Isolated         bool           `json:"isolated"`         // timed-isolated:* runs: one-off container, no session persist
	// Elevated is the operator /root elevation signal: routd sets it true ONLY
	// for a turn an operator raised via /root (steer.go cmdRoot). runed forwards
	// it to container.Input.Elevated → root=Elevated (tier 0, /var/lib/groups
	// mount). A normal spawn is never root; folder shape does not grant it.
	Elevated bool `json:"elevated,omitempty"`
	// ShareReadOnly/Egress/WebPublish are the container-capability DECISIONS routd
	// resolved from the folder's acl grants (4/R: mounts/egress/web are grants, not
	// tier). runed forwards them to container.Input so buildMounts downgrades the
	// share to RO, appends "*" to the egress allowlist, and mounts the web surfaces —
	// each only when its grant holds. EgressAllowlist is the resolved crackbox
	// allowlist (network_rules ancestry); runed wires it into EgressConfig.AllowlistFn.
	// routd is the authz plane; runed has no store (spec 5/E).
	ShareReadOnly   bool     `json:"share_read_only,omitempty"`
	Egress          bool     `json:"egress,omitempty"`
	WebPublish      bool     `json:"web_publish,omitempty"`
	EgressAllowlist []string `json:"egress_allowlist,omitempty"`
	// Secrets is the folder-ancestry secret set with the trigger user's
	// user-scoped overrides overlaid (BYOA), resolved + DECRYPTED by routd (it
	// holds SECRETS_KEY; runed has no store). runed injects them as container env
	// at spawn so the agent's own LLM calls use the user's key. Empty = inject
	// nothing. Plaintext on this hop: routd→runed is the trusted compose network,
	// same boundary the brokered agent token already crosses.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// RunOutcome is the synchronous response of POST /v1/runs, returned when
// the run completes (the turn boundary). Frames arrive out-of-band during
// the run via the /v1/turns/{turn_id}/* callbacks, not here.
//
// Outcome ∈ ok|error|silent. Three orthogonal discriminators ride alongside,
// each meaning "this is NOT a turn-boundary outcome — do not treat Outcome as
// authoritative":
//   - Steered=true: a steer ack (the folder already had a live spawn; the batch
//     was written into it). routd does NOT advance the cursor.
//   - Busy=true: runed did NOT admit the run (folder busy with a dead container,
//     or the global cap is hit) and keeps no internal queue. It is neither an
//     error nor a run — routd MUST NOT advance the cursor and MUST NOT count it
//     toward the circuit breaker; the batch is re-fed on routd's next poll.
//     RunID/Outcome/SessionID are empty on a busy reject.
//   - BreakerOpen=true: rides only on the run that trips the circuit breaker.
type RunOutcome struct {
	RunID       string `json:"run_id"`
	Outcome     string `json:"outcome"` // ok|error|silent
	SessionID   string `json:"session_id"`
	Error       string `json:"error"`
	Steered     bool   `json:"steered"`
	Busy        bool   `json:"busy"`
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

// StopRunRequest is the body of POST /v1/runs/stop: the operator-kill path
// (routd's /stop command). runed maps the folder to its live spawn and kills
// it. By folder, not run_id, because routd never persisted the run_id of a
// folder's live spawn — the folder IS the operator's handle.
type StopRunRequest struct {
	Folder string `json:"folder"`
}

// StopRunResponse reports whether a live spawn was found + killed. killed=false
// is the no-active-container case (routd renders gated's exact /stop text).
type StopRunResponse struct {
	Killed bool   `json:"killed"`
	RunID  string `json:"run_id"`
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

// RecentSessionRecord is one GET /v1/sessions/recent entry: the full
// session_log row routd needs for the new_session continuity hint and the
// inspect_session tool. Carries group_folder + error (which the dashd-facing
// SessionRow omits) so it round-trips a core.SessionRecord losslessly.
type RecentSessionRecord struct {
	ID           int64  `json:"id"`
	GroupFolder  string `json:"group_folder"`
	SessionID    string `json:"session_id"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at"`
	Result       string `json:"result"`
	Error        string `json:"error"`
	MessageCount int    `json:"message_count"`
}

// RecentSessionsResponse is GET /v1/sessions/recent — the n newest session_log
// rows for a folder, newest first.
type RecentSessionsResponse struct {
	Sessions []RecentSessionRecord `json:"sessions"`
}

// Err is the uniform JSON error envelope across the /v1/* surface.
type Err struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

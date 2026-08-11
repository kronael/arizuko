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
	// Kind selects the post-claim executor ('agent' | 'hold' | ..., spec
	// 5/8 "Filesystem restore claims the folder's run slot"). Empty = 'agent'
	// (the column default), so every existing caller is unaffected. A
	// non-agent kind carries no MessageBatch and must never be steered into
	// a live agent's running container, nor count toward its breaker.
	Kind string `json:"kind,omitempty"`
	// Elevated is the operator /root elevation signal: routd sets it true ONLY
	// for a turn an operator raised via /root (steer.go cmdRoot). runed forwards
	// it to container.Input.Elevated → root=Elevated (tier 0, /var/lib/groups
	// mount). A normal spawn is never root; folder shape does not grant it.
	Elevated bool `json:"elevated,omitempty"`
	// ShareReadOnly/Egress/WebPublish are the container-capability DECISIONS routd
	// resolved from the folder's acl grants (5/33: mounts/egress/web are grants, not
	// tier). runed forwards them to container.Input so buildMounts downgrades the
	// share to RO, appends "*" to the egress allowlist, and mounts the web surfaces —
	// each only when its grant holds. EgressAllowlist is the resolved crackbox
	// allowlist (network_rules ancestry); runed wires it into EgressConfig.AllowlistFn.
	// routd is the authz plane; runed has no store (spec 5/E).
	ShareReadOnly   bool     `json:"share_read_only,omitempty"`
	Egress          bool     `json:"egress,omitempty"`
	WebPublish      bool     `json:"web_publish,omitempty"`
	EgressAllowlist []string `json:"egress_allowlist,omitempty"`
	// Secrets is the trigger user's own MODEL credentials (store.EnvProfileKeys),
	// resolved + DECRYPTED by routd (it holds SECRETS_KEY; runed has no store).
	// runed injects them as container env at spawn so the agent's own LLM calls use
	// the user's key. Capability credentials are NOT in here and never enter the
	// container — they are brokered per tool call (spec 5/13 §Trust model). Empty =
	// inject nothing. Plaintext on this hop: routd→runed is the trusted compose
	// network, same boundary the brokered agent token already crosses.
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
//   - Busy=true: nothing ran. Either runed did NOT admit the run (folder busy
//     with a dead container, or the global cap is hit), or it admitted one that
//     a DELETE killed before it could launch. runed keeps no internal queue, so
//     in both cases this is neither an error nor a run — routd MUST NOT advance
//     the cursor and MUST NOT count it toward the circuit breaker; the batch is
//     re-fed on routd's next poll. RunID/Outcome/SessionID are empty.
//   - BreakerOpen=true: rides only on the run that trips the circuit breaker.
//
// Terminal=true qualifies Outcome=error: runed decided that a retry
// cannot repair the failure (the container invocation itself failed — a
// start error, or docker exit 125/126/127). routd obeys — it skips the turn
// retry and surfaces Error, the real cause, to the user. The DECISION
// travels, not the raw exit code: runed owns the container and its
// exit-code semantics, and routd must never classify runed's error prose
// (spec 5/12, BUGS F73). The zero value means retryable, so an older
// runed keeps today's retry behavior.
type RunOutcome struct {
	RunID       string `json:"run_id"`
	Outcome     string `json:"outcome"` // ok|error|silent
	SessionID   string `json:"session_id"`
	Error       string `json:"error"`
	Steered     bool   `json:"steered"`
	Busy        bool   `json:"busy"`
	BreakerOpen bool   `json:"breaker_open"`
	Terminal    bool   `json:"terminal,omitempty"`
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

// HoldRequest is the body of POST /v1/holds: claim Folder's run slot for an
// external, folder-exclusive job — a filesystem restore, a vacuum, a
// migration (spec 5/8 "Filesystem restore claims the folder's run slot").
// While the hold stands no agent turn can start in that folder; routd's
// dispatch gets Busy and re-feeds from its own queue.
//
// Reason is recorded on the spawn row (spawns.topic) so an operator reading
// dashd's runed page sees WHY the folder is held. No new state: a hold is a
// spawns row like any other run.
type HoldRequest struct {
	Folder types.Folder `json:"folder"`
	Reason string       `json:"reason"`
}

// HoldOutcome is POST /v1/holds' response, returned as soon as the slot is
// claimed. That is why holds are their own endpoint rather than a Kind on
// POST /v1/runs: RunOutcome's pinned contract is "returned when the run
// completes (the turn boundary)", and a hold's whole point is to hand the
// caller a handle while the run is still open.
//
// RunID is that handle — release with DELETE /v1/runs/{run_id}, the existing
// kill route, which dispatches by the spawn's recorded kind. Busy=true means
// the folder already had a live run (an agent turn or another hold) and
// NOTHING was claimed; RunID is empty and the caller must not proceed.
//
// A holder that dies without releasing does not wedge the folder: runed's
// RunTTL (RUNED_RUN_TIMEOUT) expires the hold, the same ceiling that bounds
// an agent turn.
type HoldOutcome struct {
	RunID string `json:"run_id"`
	Busy  bool   `json:"busy"`
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

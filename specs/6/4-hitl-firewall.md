---
status: draft
---

# HITL Firewall — Human-in-the-loop gate for agent actions

A generic mechanism that intercepts MCP tool calls from agents, holds
high-risk ones in a review queue, and only dispatches them after a human
approves via dashd. Not publishing-specific: any gated action (publish,
money transfer, memory delete, external API call) flows through the same
hold-and-approve primitive.

## Problem

Agents today either can call a tool or they can't — grants decide binary
access. There's no "ask the human first" state. For autonomous work
(social posting, transactions, irreversible ops) the safe default is
always-held-until-approved, but building that per-tool leads to N
half-implementations with inconsistent UX.

## Design

**Firewall sits between the MCP socket and the tool handlers.** When an
agent invokes a tool, the firewall checks grants for a `hold` marker. If
held, the call is recorded in a queue and the MCP response is a pending
id, not the real result. A human reviews and either approves (call runs,
result is written back so the agent can fetch it next turn) or rejects
(call is discarded, agent gets an explanatory failure).

**One primitive: `pending_action`.** A row in the shared SQLite that
captures the full tool invocation (tool name, args JSON, caller identity,
created_at) plus review state (status, reviewer, reviewed_at, result).
Everything else is UI on top of this table.

### Schema sketch

```
pending_actions(
  id            TEXT PK,
  group_folder  TEXT,
  caller_agent  TEXT,         -- which agent inside the group
  tool          TEXT,
  args          TEXT,         -- JSON
  status        TEXT,         -- held | approved | rejected | executed | failed
  created_at    TEXT,
  reviewed_by   TEXT NULL,
  reviewed_at   TEXT NULL,
  reviewer_note TEXT NULL,
  result        TEXT NULL,    -- JSON, populated after execute
  error         TEXT NULL
)
```

No separate database, no second daemon. Same `messages.db` the rest of
the system uses.

### Grant semantics

`grants/` already decides who can call what. Two new markers on a grant:

- `hold: true` — always queue, never auto-execute
- `hold_if: <cel-expr>` — queue when the predicate matches (e.g. amount > 10)

If neither is set, the call executes inline as today. This preserves the
current behavior for every existing tool.

**The hold flag is also a skill-level concern.** A skill that wraps a
held tool must advertise the review gate in its documentation so the
agent plans correctly — "this call will be held for review, expect a
pending response, check back next turn for the result". Skills that
assume immediate dispatch will generate confusing behavior when their
underlying tool is held. See `specs/6/5-authoring-product.md` for how
the author product's `publish` skill handles this.

### Execution flow

1. Agent calls MCP tool `publish(...)`.
2. `ipc/` dispatcher checks grants. Finds `hold: true`.
3. Writes `pending_actions` row with status=`held`. Returns
   `{ pending: true, id: "pa-abc123" }` to the agent.
4. Agent sees pending response, records the id, may continue working.
5. A human opens `/dash/review` in dashd. Sees the held action with
   tool name, rendered args, agent context.
6. Human clicks approve. Status flips to `approved`. A dispatcher
   (running inside `ipc/` or a small poller) picks up approved rows,
   actually invokes the tool handler, writes result/error back, flips
   to `executed` or `failed`.
7. Agent (next turn or via `get_pending_action(id)` MCP tool) sees the
   resolved result.

### Reviewer surface

A new dashd route `/dash/review` — HTMX, same pattern as existing
dashboards. Lists `pending_actions WHERE status='held'` ordered by
created_at. Per-row: tool, pretty-printed args, agent, age, approve /
reject buttons, edit button that opens a modal to rewrite the args
before approving (e.g. tweak a draft body).

Authorization reuses dashd's existing JWT / refresh_token session —
whoever can read dashd can review. Later we can gate per-role.

### Agent-facing surface

Agents get one new MCP tool:

- `get_pending_action(id)` — fetch status + result of a previously
  held call.

Plus the implicit behavior: any tool whose grant is marked hold returns
`{ pending: true, id }` instead of the real result.

## Relation to existing primitives

- **`grants/`** — decides which tools hold. Extend grant schema with
  `hold` / `hold_if`.
- **`ipc/`** — dispatches MCP calls. Extend to intercept → queue → return
  pending.
- **`dashd`** — new `/dash/review` screen on top of `pending_actions`.
- **`scheduled_tasks`** — orthogonal. For "approve now, execute at time
  T", create a scheduled_task that flips the `pending_actions` row from
  `approved` → `executed` at its fire time. Reuses the existing cron
  infrastructure.

No new daemon. No second DB. One new table, one new column on grants,
one new dashd screen, one new MCP tool, and an interception hook in the
`ipc/` dispatcher.

## Open questions (next phase)

1. **Dispatcher location.** Should `pending_actions WHERE status='approved'`
   be polled by `gated`, by `ipc/` as a background goroutine, or by a
   dedicated tiny daemon (`holdd`)? Polling in `gated` keeps the service
   list shorter; a dedicated daemon is cleaner isolation.

2. **Execution identity.** When a held call is approved and later
   executed, whose identity runs it — the original agent's (so grants
   re-check as if the agent called now) or the reviewer's (so the
   reviewer's grants apply)? Tentatively: original agent, but record
   reviewer in the audit log.

3. **Timeouts.** Should held actions expire if not reviewed within N
   hours? Default policy TBD. Probably per-tool, set on the grant.

4. **Editable args.** If a reviewer edits args before approving, do we
   log the diff? Store both original and edited? Needed for audit.

5. **Agent awareness of pending.** When the agent gets `{ pending: true }`,
   how much context should the firewall inject back into the next prompt
   so the agent can reason about what's pending? Options: nothing (agent
   polls), a `<pending>` XML block listing open items, or auto-inject
   results when they resolve.

6. **Non-MCP callers.** Some tool calls come from `timed` (scheduled
   tasks), not from agents. Does the firewall apply? Probably not —
   scheduled tasks are already human-authored intent. Need a `source`
   field on `pending_actions` to distinguish.

7. **UI density.** If the queue gets long (100+ pending), `/dash/review`
   needs filters by tool, by group, by age. Deferred until we actually
   hit that scale.

8. **Rejection semantics for the agent.** When a call is rejected, what
   does the agent see? A tool error with the reviewer's note? A
   special-case "rejected" response? Agents should be able to adapt
   their plan, not retry blindly.

9. **Cross-instance review.** Today each instance has its own dashd.
   For a human managing multiple instances (REDACTED + REDACTED + REDACTED),
   a single review inbox across instances is nice-to-have. Punt.

10. **Batching.** Can a reviewer approve N related held actions in one
    click (e.g. a thread of 5 posts)? Group-by-tag? Deferred.

## Out of scope

- Rejecting with a counter-proposal (reviewer writes alternative args
  that become a new pending for the agent to accept) — interesting but
  not v1.
- Multi-reviewer approval flows (2-of-3 sign-off). Overkill for a solo
  operator.
- Cryptographic proof of review (signed approvals). Not needed until we
  have untrusted reviewers.

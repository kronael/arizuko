---
status: draft
---

# workflowd — Declarative workflows as a sibling daemon

A small daemon that reads workflow definitions (TOML files), watches
the shared SQLite bus for trigger events, and executes ordered steps
that are themselves calls to existing arizuko primitives: MCP tools,
message inserts, scheduled tasks, agent delegations, HITL pending
actions. Workflows are data, not code — adding one means dropping a
file, not touching a binary.

## Problem

1. **Repeated glue.** Evangelist, authoring, onboarding all look the
   same in the abstract: _when X happens, do A, then B, then maybe C_.
   Today each is a bespoke daemon or a skill whose prompt hand-holds
   the agent through the sequence. Skills are the wrong layer for
   "always do these three steps in this order" — the agent deviates.
2. **Scheduler is single-shot.** `timed` fires a prompt at a cron time
   with no notion of follow-up. "Every morning draft three posts, then
   8h later, if any are un-approved, nudge me" is a workflow, not a
   task.
3. **Event → action without an agent.** Some reactions don't need an
   LLM turn: "verb=join → send welcome, log diary, schedule 24h
   follow-up" is three MCP calls. Spinning a container is wasteful.

Prior fragments (thin — user's richer sketches not recovered):

- `specs/5/2-agent-pipeline.md` splits the concept into **workflows**
  (subagents via Agent tool) vs **orchestration** (inter-group slink).
  Defers the declarative form, assumes skills carry topology.
- Deleted `specs/v2/workflows.md` (git `c7f27e3`, 42 lines) sketched
  agent-side media processing and "sub-workflows / sub-groups."
- `refs/ironclaw/skills/ironclaw-workflow-orchestrator/` (kanipi ref)
  is closest prior art: event-driven routines with `trigger_type`,
  `event_source`, `event_filters`, `action_type`, prompt templates.

None define a runtime that lives outside the agent. This spec does.

## Design

**A workflow is a file.** `<name>.flow.toml` under the instance data
dir's `workflows/`, or a group folder for group-scoped flows.
`workflowd` loads on startup, polls for changes, registers triggers
against the bus.

```toml
name = "welcome-new-member"

[trigger]
type  = "message"    # message | cron | webhook | manual | workflow
verb  = "join"       # filter on messages.verb
group = "REDACTED/*"

[[step]]
id   = "greet"
tool = "send_message"
args = { chat_jid = "${trigger.chat_jid}", text = "Welcome, ${trigger.sender_name}!" }

[[step]]
id     = "assign"
tool   = "set_route"
args   = { jid = "${trigger.chat_jid}", target = "REDACTED/newbies" }
unless = "${trigger.sender_is_bot}"

[[step]]
id   = "followup"
tool = "schedule_task"
args = { cron = "in 24h", prompt = "Check on ${trigger.sender_name}" }
```

The file is the whole contract. `workflowd` parses the TOML, resolves
`${...}` bindings against the trigger payload and prior step outputs,
and dispatches each step via the existing primitive.

### Trigger taxonomy

All triggers land as SQL the daemon can poll — the only transport is
`messages.db`.

| Type       | Fires when                                      | Source                       |
| ---------- | ----------------------------------------------- | ---------------------------- |
| `message`  | Row matching `verb`/`group`/`chat_jid` appears  | `messages`                   |
| `cron`     | Wall-clock expression elapses                   | `scheduled_tasks` (see note) |
| `webhook`  | Row inserted via HTTP API                       | `messages` (synthetic)       |
| `manual`   | Operator clicks run in dashd, or calls MCP tool | ad-hoc insert                |
| `workflow` | Another workflow finishes with status=completed | `workflow_runs`              |

`cron` is deliberately **not** a second scheduler: workflowd writes a
row to `scheduled_tasks` with sender `workflow:<name>` and when `timed`
fires it, the resulting message matches a `message` trigger. `timed`
stays the single source of wall-clock truth.

### Step taxonomy

A step points at an existing primitive plus arguments:

- `tool = "<name>"` — call an MCP tool via `ipc/` as the workflow's
  identity. The common case; `send_message`, `schedule_task`,
  `set_route`, `publish`, etc. are already tools.
- `message = { chat_jid, text }` — shorthand for a raw message insert.
- `agent = { group, prompt }` — invoke a gated run, wait for
  completion, capture output. The only step that spawns a container.
- `workflow = "<name>"` — call a sub-workflow, bind its result.
- `branch = [...]` — explicit `when`/`then`/`else`.

Sequential by default. `parallel = true` on a block runs concurrently.

### Variable binding

A minimal substitution language, not a full expression engine:

- `${trigger.field}` — trigger payload (message row, cron metadata).
- `${step.<id>.field}` — prior step output.
- `${env.NAME}` — allowlisted env vars.
- Conditionals: `when`, `unless`. Operators: `==`, `!=`, `&&`, `||`,
  `!`. No arithmetic, no function calls.

Deliberately **not** Turing-complete. If a workflow needs loops or
arithmetic, it calls an `agent` step — that's what agents are for.

### Schema sketch

Two new tables in `messages.db`, no second store.

```
workflows(
  name         TEXT PRIMARY KEY,
  path         TEXT NOT NULL,       -- source file path
  hash         TEXT NOT NULL,       -- content hash (file change detection)
  enabled      INTEGER DEFAULT 1,
  owner        TEXT,                -- group folder or NULL for instance-wide
  loaded_at    TEXT
)

workflow_runs(
  id           TEXT PRIMARY KEY,
  workflow     TEXT NOT NULL,
  trigger_kind TEXT NOT NULL,       -- message | cron | webhook | manual | workflow
  trigger_ref  TEXT,                -- message_id / task_id / parent run_id
  status       TEXT NOT NULL,       -- pending | running | completed | failed | cancelled
  started_at   TEXT,
  ended_at     TEXT,
  current_step TEXT,                -- step id for resumability
  state        TEXT,                -- JSON: step outputs, bindings
  error        TEXT
)
```

Step outputs live inside `workflow_runs.state` as JSON, keyed by step id.
Observability = `workflow_runs` + a dashd screen. No separate log table
until we hit volume.

### Execution model

`workflowd` has two loops:

1. **Trigger loop** — every ~2s, scan active triggers for new matches:
   unseen `messages` rows, `scheduled_tasks` fires with `workflow:`
   sender, completed `workflow_runs` that are parents of chained flows.
   Each match inserts a `workflow_runs` row with `status=pending`.
2. **Execution loop** — pick up pending runs, drive step by step,
   persist state on each boundary. On crash/restart, `running` rows
   resume from `current_step`.

Blocking steps (agent call, held pending action) yield: the run flips
to `waiting` with a `wait_on` reference and is re-driven when the
referenced thing completes. Long-lived workflows stay cheap.

### Authoring, identity, auth

Operators edit TOML in `workflows/`, save, and `workflowd` reparses on
its next poll. Invalid files log and keep the previous version live.
No deploy step.

Each workflow has an `owner` (group folder or instance-wide). Tool
steps run as that owner; `ipc/` applies normal grants checks. No
`publish` grant for the group means no `publish` for the workflow.

HITL: if a tool step hits a held grant, the workflow flips to
`waiting` on the `pending_action` id and resumes with the result bound
once the action resolves. Review gates are just slow steps.

## Relation to existing primitives

- **`specs/5/2-agent-pipeline.md`** — supersedes its declarative-workflow
  direction. "Subagents inside one container" survives as the `agent`
  step type; inter-group slink orchestration survives as a normal tool
  call to a remote group.
- **`specs/1/a-task-scheduler.md`** / **`specs/4/8-scheduler-service.md`**
  — `timed` remains the single cron daemon. `workflowd` never schedules
  wall-clock time itself; it writes `scheduled_tasks` with a `workflow:`
  sender and lets timed fire them.
- **`specs/6/4-hitl-firewall.md`** — workflows are natural callers for
  held tools. Nothing in the firewall changes; workflowd respects the
  pending response and waits.
- **`specs/6/5-authoring-product.md`** — the author product's "draft →
  review → publish → archive" pattern is a workflow. Shipping workflowd
  lets the product be a flow file instead of a bundle of skill prompts
  that _might_ chain correctly.
- **`specs/4/21-onboarding.md`** — `onbod` is a bespoke state machine;
  a future version could be flow files + workflowd. Retiring that
  daemon is a validation target for v2, not v1.
- **`specs/4/R-evangelist.md`** — first-class use case. The evangelist
  is a long-running workflow per platform.
- **`grants/`** — unchanged. Workflow identity goes through existing
  grant checks at every tool call.

## Minimum viable cut

**v1 scope:**

- `workflowd/main.go`, sibling daemon to `timed` and `gated`, own
  binary.
- `workflows` and `workflow_runs` tables via `workflowd/migrations/`.
- TOML parser + binding resolver. One format, no alternatives.
- Triggers: `message`, `cron`, `manual`. No webhook, no chained.
- Steps: `tool`, `message`, `branch`. No `agent`, no sub-workflow,
  no `parallel`.
- Example flow in `template/workflows/welcome.flow.toml`.
- Read-only `/dash/flows` screen listing registered workflows and
  recent runs. Filesystem is the editor.

**Out of v1:** inotify hot-reload (poll instead), sub-workflows,
workflow-chain triggers, parallel steps, agent step, rollback /
compensating actions, secrets beyond env allowlist.

## Open questions (next phase)

1. **Flow file location.** Instance-wide (`/srv/data/arizuko_<name>/
workflows/`), per-group (`groups/<folder>/workflows/`), or both
   with inheritance? Per-group ties workflows to products; instance-
   wide is simpler. User's prior thinking on this was not recoverable.

2. **TOML vs YAML vs s-expr vs custom.** User called it "language-
   like." In descending boringness: (a) TOML with `[[step]]` arrays —
   what this spec uses; (b) YAML — richer nesting, indentation pain;
   (c) minimal s-expression — cleanest conditionals, nobody likes
   parens; (d) custom syntax — high cost, zero ecosystem. Recommend
   TOML for v1; revisit if conditionals get ugly.

3. **Binding language power.** Currently `==`, `!=`, `&&`, `||`, `!`
   only. Do we need arithmetic? Substring? `matches` for regex? Each
   addition moves toward a real expression language. Keep boring; add
   only when a real flow demands it.

4. **Agent step semantics.** Does `agent = { group, prompt }` (a)
   enqueue into the group's inbound queue and wait for reply, (b)
   invoke gated directly with a synthetic sender, or (c) spawn an
   ephemeral container outside the group model? (a) reuses everything
   but is indirect; (c) is cleanest but adds a new container lifecycle.

5. **Failure modes and retries.** Per-step `on_failure = "abort" |
"retry" | "skip"` with abort as default? What about partial state —
   does the agent that triggered a workflow see anything if step 3 of
   5 fails?

6. **Who can author.** Filesystem-as-editor means "whoever can write
   to the workflows dir." Root only? Per-group? An MCP tool
   (`create_workflow`) that routes proposals through HITL before
   landing? "Agent authors its own workflows" is powerful and the
   biggest foot-gun.

7. **Trigger fan-out.** If two workflows match the same event, do
   both run? In parallel? Declared order? First match wins? Ironclaw's
   routines fire independently — probably right, but two welcome
   messages to the same join is a classic bug. Consider `priority` /
   `group` fields for short-circuit.

8. **Observability depth.** `workflow_runs.state` JSON is minimum. Do
   we need a per-step log table for timing/inputs/outputs/errors? One
   row per step, or one log per run? Deferred until volume matters.

9. **Cancellation.** Flip status to `cancelled`, but what about the
   mid-execution step? Let it finish, or abandon? Tool calls that
   already committed (sent a message) have no undo — only compensating
   actions.

10. **Skills vs workflows.** Skills = prompt-level, agent-deliberated.
    Workflows = deterministic, agent-free. Is the line clean, or will
    we get the same logic in both layers (a `/publish` skill and a
    `publish-flow`)? Affects how the authoring product decomposes.

11. **Prior fragments are thin.** Recovered: ironclaw routines (not
    arizuko-native), a 42-line deleted `specs/v2/workflows.md` on
    media. If the user has richer sketches in notes/memory, pull them
    in before v1 freezes. This draft may need a pass against whatever
    the user actually remembers.

12. **workflowd vs gated-internal.** New daemon or goroutine inside
    gated? Per the architecture memo ("microservices are easy with AI
    agents"), new daemon is the assumed direction. Noted for
    completeness — decision is "new daemon" unless a reviewer objects.

## Out of scope

- **Turing completeness.** Loops live inside agent steps. The DSL will
  never grow `for`/`while`.
- **GUI workflow editor.** Filesystem + text editor. A dashd read-only
  viewer is in scope; a drag-and-drop builder is not.
- **Versioning / rollback of flow definitions.** git is the version
  control system; workflowd sees whatever is on disk.
- **Cross-instance workflows.** One workflowd per instance. Cross-
  instance chaining happens at the message-bus level (slink).
- **Replay of past runs.** `workflow_runs.state` is for audit only;
  re-running a completed workflow is a new run.

---
status: deferred
---

# workflowd — declarative workflows

Sibling daemon to `gated`/`timed`. Reads `<name>.flow.toml` files from
`workflows/`, watches shared SQLite for triggers, executes ordered
steps calling existing MCP tools / message inserts / schedule_task /
agent runs.

Example:

```toml
name = "welcome-new-member"
[trigger]
type = "message"; verb = "join"; group = "REDACTED/*"

[[step]]
id = "greet"
tool = "send_message"
args = { chat_jid = "${trigger.chat_jid}", text = "Welcome, ${trigger.sender_name}!" }

[[step]]
id = "followup"
tool = "schedule_task"
args = { cron = "in 24h", prompt = "Check on ${trigger.sender_name}" }
```

Triggers: `message`, `cron` (writes to `scheduled_tasks` with
`workflow:<name>` sender — `timed` stays the single cron), `webhook`,
`manual`, `workflow`. Step types: `tool`, `message`, `agent`,
`workflow`, `branch`; sequential default, `parallel = true` block.

Bindings: `${trigger.x}`, `${step.<id>.x}`, `${env.X}`; operators
`==`/`!=`/`&&`/`||`/`!`; `when`/`unless`. Deliberately not
Turing-complete — loops go inside `agent` steps.

Schema: `workflows(name PK, path, hash, enabled, owner, loaded_at)`
and `workflow_runs(id PK, workflow, trigger_kind, trigger_ref, status,
started_at, ended_at, current_step, state JSON, error)`. State lives
in `workflow_runs.state` JSON.

Identity = workflow's `owner`. Tool steps run with that group's
grants; HITL-held steps flip status to `waiting` and resume when
resolved.

Rationale: evangelist, authoring, onboarding share the same shape
(event → ordered actions). Skills are the wrong layer for
deterministic sequences — the agent deviates. Replaces the
declarative-workflow direction from
[../5/2-agent-pipeline.md](../5/2-agent-pipeline.md).

Unblockers (v1 cut): TOML parser + binding resolver,
`message`/`cron`/`manual` triggers only, `tool`/`message`/`branch`
steps, one example flow, read-only `/dash/flows` screen, poll (no
inotify). Out of v1: sub-workflows, chained triggers, parallel,
`agent` step, rollback.

Open: flow file location (instance-wide vs per-group), failure
policy (abort/retry/skip), authorship gate, trigger fan-out ordering,
workflowd as daemon vs gated goroutine.

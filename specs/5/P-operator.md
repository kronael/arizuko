---
status: planned
---

# Telephone Operator (Proactive Agent)

A system-level agent that monitors events across all groups and initiates
conversations with the operator — not just responds when messaged. Like an
assistant that calls you when something needs attention.

---

## Problem

Today the system is reactive: it only communicates outward when the operator
messages it or when a timed digest fires. There is no component that:

- Notices something interesting happened in group X and tells you
- Fires an alert when an agent errors repeatedly
- Sends a health summary without being asked
- Is reachable by the operator anytime for a current status

The `notify/` package exists and is used by `gated` and `onbod` for
error/event messages, but those are fire-and-forget strings, not an agent
with reasoning. The `control-chat` spec (specs/7/20) covers the root group
as a two-way control channel; this spec is about a dedicated operator agent
that bridges monitoring events into that channel with judgment.

---

## Design

### Operator group

The operator group is a regular `groups` row at tier-0 (no
parent). It is distinguished by a config flag, not a separate table:

```sql
ALTER TABLE groups ADD COLUMN is_operator BOOLEAN NOT NULL DEFAULT false;
```

Only one operator group per instance. The operator agent's system prompt
(SOUL.md) gives it cross-group visibility and proactive personality.

The operator group JID is stored in `router_state` (`key=operator_jid`) so
any daemon can retrieve it without scanning the table.

### Proactive message delivery

"Calling the user" means sending a message to the operator's JID on a
messaging platform — the same JID the operator normally uses to chat with
the agent. That JID is the `chat_jid` of the root group's primary chat.

The operator agent sends via the `send_message` MCP tool. From the agent's
perspective this is identical to replying in a conversation. The platform
(Telegram, Discord, etc.) delivers it as an unprompted message.

No new channel mechanism is needed. The operator group is already registered
with a JID; `gated` can route outbound messages to it.

### Trigger sources

Three categories of triggers initiate operator agent runs:

#### 1. Error events (immediate)

When `gated` fires `SetNotifyErrorFn` (circuit breaker opens, persistent
agent error), instead of only sending a raw string via `notify.Send`, also
inject a system message into the operator group:

```go
g.store.InsertSysMsg(operatorFolder, "gated", "agent_error",
    fmt.Sprintf(`<error group="%s">%s</error>`, folder, errText))
```

The next `pollOnce` for the operator group picks up the system message and
runs the operator agent. The agent sees the error event and decides whether
to notify the user (it might suppress noise if the same group errored 30
seconds ago).

#### 2. Timed health checks (scheduled)

A `scheduled_tasks` row owned by the operator group, e.g. cron `0 * * * *`
(hourly). Prompt: `<health_check>Scan group states, pending tasks, recent
errors. Report only if something needs attention.</health_check>`.

The operator agent reads `get_groups`, checks session logs, decides if a
message is warranted. If nothing is wrong, it returns without sending.

#### 3. Cross-group event subscriptions (future)

Listener groups (spec 1-listener.md) could post digest summaries to the
operator group rather than directly to the user. The operator agent decides
what to escalate. Not in scope for this spec; the mechanism (injecting a
system message into the operator group) is the same.

### Operator agent grants

The operator group is tier-0 so it gets `*` grants by default (all MCP
tools). It needs:

- `get_groups` — list all registered groups and their states
- `read_file(path=groups/*/logs/*)` — read recent container logs
- `send_message(jid=*)` — send to any JID (to reach the user on any channel)
- `list_tasks`, `get_task` — inspect scheduled tasks
- Standard `send_reply`, `send_document`

No grant rule changes needed beyond the existing tier-0 defaults.

### Operator agent SOUL

The operator agent's `SOUL.md` should establish:

- Brief and direct. No filler. Only send when something needs attention.
- When reporting an error: one sentence what broke, one sentence what the
  operator should do (or "monitoring, no action needed").
- When doing a health check: only send a message if there is something to
  act on. Silence is the default success signal.
- When the operator messages directly: respond like a concise status board.

Example SOUL excerpt:

```
You are the system operator for this arizuko instance.

You monitor all groups and alert the operator only when action is warranted.
When in doubt, stay silent. One short message beats a wall of text.

On error events: state which group, what failed, whether it is retrying.
On health checks: summarize only anomalies. No news is good news.
On direct messages: give current system state concisely.
```

### Relation to existing notify library

`notify/` remains as-is for simple string broadcasts (e.g. onboarding
events). The operator agent complements it: for events that require
reasoning (should I wake the operator at 3am for this?), inject a system
event and let the agent decide.

Pattern:

- Trivial event (new user onboarded) → `notify.Send` directly
- Consequential event (circuit breaker open, agent stuck) → `InsertSysMsg`
  into operator group + let agent decide

Both can be used together: `notify.Send` for immediate raw notification,
`InsertSysMsg` for the agent's follow-up analysis.

### Initialization

`arizuko create <name>` seeds an operator group if none exists:

1. Creates `groups/operator/` folder
2. Inserts `groups` row with `is_operator=true`, `mode=active`
3. Writes default `SOUL.md` (brief, proactive, no filler)
4. Stores JID in `router_state` key `operator_jid`
5. Registers a default hourly health-check task

Operator can disable health checks by pausing the task.

---

## Schema changes

```sql
ALTER TABLE groups ADD COLUMN is_operator BOOLEAN NOT NULL DEFAULT false;
-- operator_jid stored in router_state (existing key-value table)
```

---

## Affected code

| Location              | Change                                                             |
| --------------------- | ------------------------------------------------------------------ |
| `core/types.go`       | Add `IsOperator bool` to `Group`                                   |
| `store/store.go`      | Read `is_operator`, add `OperatorGroup()` query                    |
| `gateway/gateway.go`  | On error events, `InsertSysMsg` to operator group                  |
| `notify/notify.go`    | No change (still used for direct string delivery)                  |
| `cmd/arizuko/main.go` | Seed operator group on `create`, `--operator` flag for `group add` |
| `template/`           | Add `SOUL.md` for operator group                                   |

---

## Open questions

**Q1: What if the operator group's agent errors?**
Error notification for the operator group falls back to `notify.Send` only
(the raw string path). No recursive operator-operator loop.

**Q2: Can there be multiple operator groups (multi-operator)?**
Not in this spec. `is_operator=true` is a single-row constraint. Multi-operator
is the domain of role-based access (future, noted in 20-control-chat.md).

**Q3: Should the operator agent have its own JID per channel, or share the
root group's JID?**
Share the root group's JID for now. The operator group IS the root group in
most deployments. If separate, register a distinct JID — nothing in the
design prevents it.

**Q4: Suppression / deduplication of error events**
If 10 groups error in the same minute, the operator agent receives 10 system
events and may produce 10 messages. The agent's SOUL.md should instruct it
to batch — but that is prompt design, not schema. A `last_notified_at` field
on `chats.errored` could support rate-limiting at the `InsertSysMsg` call
site if noise proves to be a problem.

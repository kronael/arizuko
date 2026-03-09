# Hierarchical Group Routing

Any group can define routing rules for child groups beneath
it. A parent group decides how inbound messages flow to
its children — statically via rules, or dynamically via
IPC delegation from its agent.

Builds on `specs/1/e-worlds.md` (Phase 1 shipped: `/`
separator, `isRoot()`, glob matching via minimatch).

---

## Group tree model

Group folder hierarchy mirrors a file path:

```
main          ← root (isRoot = true)
main/code     ← child of main
main/code/py  ← grandchild
team          ← another root
team/alice    ← child of team
team/bob      ← child of team
```

`parent` = folder with one fewer segment. Root groups
have no parent. A group is a parent if any registered
group has it as a prefix.

**Channel bindings** live on the parent (or root). Child
groups have no JID of their own — they are addressed by
folder. Responses from children are sent to the originating
JID.

---

## Routing rules

A parent group holds a JSON array `routing_rules`. Each
rule maps a condition to a target child folder.

```typescript
type RoutingRule =
  | { type: 'command'; trigger: string; target: string }
  | { type: 'pattern'; pattern: string; target: string }
  | { type: 'keyword'; keyword: string; target: string }
  | { type: 'sender'; pattern: string; target: string }
  | { type: 'default'; target: string };
```

| Field     | Description                                      |
| --------- | ------------------------------------------------ |
| `type`    | Rule kind (command / pattern / keyword / sender) |
| `trigger` | Command prefix (`/code`, `/research`)            |
| `pattern` | Regex against full message text                  |
| `keyword` | Substring match (case-insensitive)               |
| `target`  | Target child folder (`main/code`, `team/alice`)  |

`sender` matches against the sender JID or display name.

### Evaluation order

For each inbound message on a parent-bound JID:

1. **Exact command** — message starts with `trigger` value
2. **Pattern** — regex matches message text
3. **Keyword** — case-insensitive substring in message
4. **Sender** — regex matches sender name or JID
5. **Default** — catch-all, if present
6. **No match** — message handled by parent group itself

First matching rule wins. Rules evaluated in array order
within each type tier.

---

## IPC delegation

A parent's agent can delegate dynamically at runtime by
writing an IPC request:

```json
{
  "id": "1709693200000-abc123",
  "type": "delegate_group",
  "group": "main/code",
  "prompt": "Fix the type error in src/db.ts",
  "chatJid": "tg:-100123456"
}
```

| Field     | Description                              |
| --------- | ---------------------------------------- |
| `group`   | Target child folder (must be registered) |
| `prompt`  | Prompt to send to the child agent        |
| `chatJid` | Originating JID (for reply routing)      |

Gateway reply:

```json
{ "id": "...", "ok": true, "result": { "queued": true } }
```

The child agent's response is sent back to the originating
JID via `sendMessage`. Delegation is fire-and-queue: the
parent's current container does not wait for the child to
finish. The child runs in its own GroupQueue slot.

Authorization: direct parent → child only. Source group
must be the immediate parent of the target (exactly one
segment deeper, same world prefix). Root groups may
delegate to their own direct children only — not to
grandchildren or cross-world groups. A child cannot
delegate to siblings, ancestors, or groups in other
worlds.

---

## Schema changes

New columns on `registered_groups`:

```sql
ALTER TABLE registered_groups ADD COLUMN parent TEXT;
ALTER TABLE registered_groups ADD COLUMN routing_rules TEXT;
```

| Column          | Type | Description                            |
| --------------- | ---- | -------------------------------------- |
| `parent`        | TEXT | Parent folder, NULL for roots          |
| `routing_rules` | TEXT | JSON array of `RoutingRule[]`, NULL=[] |

`parent` is informational — routing lookups use folder
prefix matching. `routing_rules` is only read on the
parent; child rows ignore it.

Migration: existing rows get `parent = NULL`,
`routing_rules = NULL`.

`register_group` action gains optional `parent` param.
Setting `parent` does not require the parent to exist yet
(allows bottom-up registration).

---

## Gateway changes

### router.ts

Add `resolveRoutingTarget()`:

```typescript
function resolveRoutingTarget(
  msg: NewMessage,
  rules: RoutingRule[],
): string | null {
  for (const rule of rules) {
    if (rule.type === 'command') {
      if (
        msg.content.startsWith(rule.trigger + ' ') ||
        msg.content === rule.trigger
      )
        return rule.target;
    } else if (rule.type === 'pattern') {
      if (new RegExp(rule.pattern).test(msg.content)) return rule.target;
    } else if (rule.type === 'keyword') {
      if (msg.content.toLowerCase().includes(rule.keyword.toLowerCase()))
        return rule.target;
    } else if (rule.type === 'sender') {
      const s = msg.sender_name ?? msg.sender;
      if (new RegExp(rule.pattern).test(s)) return rule.target;
    } else if (rule.type === 'default') {
      return rule.target;
    }
  }
  return null;
}
```

`processGroupMessages` calls this after trigger check, before
`runContainerAgent`. If a target is found, the message is
enqueued for the child group instead.

### group-queue.ts

`GroupQueue` is already keyed by an opaque string ID.
Child groups use their folder as the queue key (not a JID).
No structural changes needed — `enqueueMessageCheck` and
`enqueueTask` accept any string key.

`processMessagesFn` is invoked with the child folder key.
The calling code must look up the child group by folder
and use the originating JID for `sendMessage` responses.

Add `delegateToChild()` helper in `index.ts`:

```typescript
async function delegateToChild(
  childFolder: string,
  prompt: string,
  originJid: string,
): Promise<void> {
  const child = registeredGroups[childFolder];
  if (!child) return; // unregistered child, silently drop
  queue.enqueueMessageCheck(childFolder);
  // store synthetic message for the child's folder/JID
}
```

---

## Examples

### 1. Root routes `/code` to child group

Root group `main` (JID `telegram:-100111`) has:

```json
{
  "folder": "main",
  "routing_rules": [
    { "type": "command", "trigger": "/code", "target": "main/code" }
  ]
}
```

Child group `main/code` registered:

```json
{ "folder": "main/code", "parent": "main", "requires_trigger": 0 }
```

Message `/code fix the null check` arrives on
`telegram:-100111`:

1. Gateway finds group `main` via JID lookup
2. `resolveRoutingTarget` matches `command` rule
3. Message enqueued for `main/code` GroupQueue slot
4. `main/code` container spawned; response sent back to
   `telegram:-100111`

The `/code` prefix can be stripped before the child sees
it (configurable per rule, default: strip).

### 2. Team parent routes by sender to per-person children

Group `team` (JID `discord:12345`) has:

```json
{
  "folder": "team",
  "routing_rules": [
    { "type": "sender", "pattern": "alice", "target": "team/alice" },
    { "type": "sender", "pattern": "bob", "target": "team/bob" },
    { "type": "default", "target": "team/shared" }
  ]
}
```

Message from Alice → routes to `team/alice` (own session,
CLAUDE.md, skills). Message from unknown → `team/shared`.

Each child maintains its own session history in
`data/sessions/team/<name>/`.

### 3. Parent agent delegates dynamically via IPC

Parent `main` processes a message: "can you deploy the
app and then summarize the logs?". The agent decides
to split the work:

```
Agent in main:
  calls mcp__nanoclaw__delegate_group
    { "group": "main/deploy", "prompt": "deploy the app" }
  calls mcp__nanoclaw__delegate_group
    { "group": "main/logs",   "prompt": "summarize recent logs" }
```

Gateway receives both delegate requests, enqueues both
child groups. They can run in parallel (subject to
`MAX_CONCURRENT_CONTAINERS`). Each child sends its
response back to the originating JID independently.

`delegate_group` is registered as a gateway action
(see `specs/1/0-actions.md`), exposed as an MCP tool
via the action manifest.

---

## Prior art

- **brainpro** (muaddib): One session per `ChannelTarget`.
  No intra-channel routing. Subagents defined in
  `.brainpro/agents/<name>.toml` with restricted tool sets
  — closest analog to child groups.
- **takopi**: Per-thread FIFO queues. Thread = routing key.
  Thread-aware routing with resume tokens. Parallel threads,
  serialized within a thread. Projects bound to chat IDs —
  static routing only.

Key difference from `specs/1/1-agent-routing.md`: that
spec covers workers within a single group (intra-group,
same JID). This spec covers routing across distinct
registered groups (inter-group, child groups with their
own session and config). The IPC `delegate` message
defined here can also drive `agent-routing.md` delegation.

---

## Error handling: static vs dynamic

**Static routing** (gateway message loop): if the target is
unauthorized or missing, the gateway logs a warning and falls
back to the parent agent. The message is not lost.

**Dynamic routing** (`delegate_group` action): if the target
is unauthorized, the action throws an error. The caller gets
an error reply via IPC. This is intentional — the agent chose
to delegate explicitly and should handle the failure.

## Open

- Strip command prefix before child sees it: per-rule flag
  or always-strip? Default proposal: strip.
- Circular delegation: `main` delegates to `main/code`
  which delegates back. Detect via delegation depth counter
  (max 3). Gateway enforces, not agent.
- Child response threading: should child responses reply-to
  the triggering message? Per-channel support varies.
- Static rules evaluated on latest message only; multi-
  message context (e.g., "route if last 3 messages from
  same sender") is out of scope for v1.
- `routing_rules` regex untrusted if operator-supplied —
  validate patterns on insert, reject malformed.
- Broadcast mode: send to multiple children (no winner).
  Out of scope for v1.
- Phase 4 (worlds.md): tree-scoped IPC auth — planned.
  Direct parent→child enforced; cross-world blocked.

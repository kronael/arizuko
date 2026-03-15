# authd

**Status**: shipped — `authd/` package (identity.go, policy.go, web.go, jwt.go, oauth.go, middleware.go)

Authorization policy engine. Consumers call it to check
whether a caller is allowed to perform an action.

## Role

authd is a pure policy engine. It answers one question:

> Can caller with identity X perform action Y on target Z?

It doesn't know what actions do. It doesn't execute anything.
It receives a query and returns allow or deny.

## Interface

Called by consumer daemons (gated, timed, etc.)
after receiving a stamped request from actid.

```
authorize(caller, action, target) → allow | deny
```

Where:

- `caller`: `{folder, tier}` — stamped by actid
- `action`: tool name (e.g. `send_message`, `schedule_task`)
- `target`: action-specific (e.g. chat_jid, task_id)

## Policy rules

### Tier-based access

Each MCP tool has a minimum tier. Lower-numbered tiers
have more privilege.

| Min tier | Tools                                                      |
| -------- | ---------------------------------------------------------- |
| 1        | `register_group`, `inject_message`, `get_routes`,          |
|          | `set_routes`, `add_route`, `delete_route`                  |
| 2        | `schedule_task`, `pause_task`, `resume_task`,              |
|          | `cancel_task`, `delegate_group`                            |
| 2+ only  | `escalate_group` (tier >= 2 only, not root/world)          |
| 3        | `send_message`, `send_file`, `list_tasks`, `reset_session` |

Tier 0 (root) can call everything. Tier 3 (worker) can
only call tier-3 tools.

### Ownership checks

Some actions require ownership validation beyond tier:

- `pause_task` / `resume_task` / `cancel_task`: caller's
  folder must match task's `owner` (tier 2), or be in same
  world (tier 1), or be root (tier 0)
- `set_routes` / `add_route` / `delete_route`: tier 1 can
  only modify routes targeting own subtree
- `delegate_group`: target must be a child of caller's folder

### Scope containment

Callers can only act within their own subtree:

- `andy/research` can act on `andy/research/*`
- `andy/research` cannot act on `andy/ops/*`
- `andy` (tier 0) can act on everything under `andy/`

## Tables owned

| Table           | Purpose                   |
| --------------- | ------------------------- |
| `auth_users`    | user accounts (web login) |
| `auth_sessions` | web session tokens        |

Migration service name: `authd`.

These tables are for web authentication, separate from
the tier-based MCP authorization which is computed from
folder depth (no tables needed).

## Flow

```
gated receives stamped request from actid:
  {tool: cancel_task, caller: {folder: "andy/research", tier: 1}, args: {taskId: "abc"}}

gated calls authd:
  authorize(caller={folder: "andy/research", tier: 1},
            action="cancel_task",
            target={taskId: "abc"})

authd checks:
  1. tier 1 ≤ min tier 2 for cancel_task? yes
  2. task "abc" owner in same world as caller? yes (world check)
  → allow

gated executes the cancellation.
```

## Layout

```
authd/
  identity.go
  policy.go
  web.go
  jwt.go
  oauth.go
  middleware.go
```

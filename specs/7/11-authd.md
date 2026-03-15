# authd

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

| Min tier | Tools                                                         |
| -------- | ------------------------------------------------------------- |
| 1        | `register_group`, `inject_message`, `escalate_group`          |
| 2        | `schedule_task`, `delete_task`, `pause_task`,                 |
|          | `resume_task`, `cancel_task`, `delegate`, `set_routing_rules` |
| 3        | `send_message`, `send_file`, `list_tasks`, `clear_session`    |

Tier 0 (root) can call everything. Tier 3 (worker) can
only call tier-3 tools.

### Ownership checks

Some actions require ownership validation beyond tier:

- `delete_task`: caller's folder must match task's `owner`
- `pause_task` / `resume_task`: same ownership check
- `set_routing_rules`: caller must own the target group
- `delegate`: caller must own the parent group

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
  {tool: delete_task, caller: {folder: "andy/research", tier: 1}, args: {task_id: "abc"}}

gated calls authd:
  authorize(caller={folder: "andy/research", tier: 1},
            action="delete_task",
            target={task_id: "abc"})

authd checks:
  1. tier 1 ≤ min tier 2 for delete_task? yes
  2. task "abc" owner = "andy/research"? yes (ownership check)
  → allow

gated executes the delete.
```

## Layout

```
services/authd/
  main.go
  migrations/
    0001-schema.sql
  authd.go
  authd_test.go
```

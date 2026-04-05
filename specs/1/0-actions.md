---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Gateway Actions

## Action table

### Messaging

| Action         | Cmd | MCP | Input                              |
| -------------- | --- | --- | ---------------------------------- |
| `send_message` | --  | yes | `{ chatJid, text, sender? }`       |
| `send_file`    | --  | yes | `{ chatJid, filepath, filename? }` |

### Session

| Action          | Cmd       | MCP | Input |
| --------------- | --------- | --- | ----- |
| `reset_session` | `/new`    | yes | --    |
| `ping`          | `/ping`   | --  | --    |
| `chatid`        | `/chatid` | --  | --    |

### Tasks

| Action          | MCP | Input                                                                 |
| --------------- | --- | --------------------------------------------------------------------- |
| `schedule_task` | yes | `{ targetJid, prompt, schedule_type, schedule_value, context_mode? }` |
| `pause_task`    | yes | `{ taskId }`                                                          |
| `resume_task`   | yes | `{ taskId }`                                                          |
| `cancel_task`   | yes | `{ taskId }`                                                          |

### Groups

| Action           | MCP | Input                                                                               |
| ---------------- | --- | ----------------------------------------------------------------------------------- |
| `refresh_groups` | yes | --                                                                                  |
| `register_group` | yes | `{ jid, name?, folder?, fromPrototype?, containerConfig?, parent?, routingRules? }` |
| `delegate_group` | yes | `{ group, prompt, chatJid, depth? }`                                                |
| `escalate_group` | yes | `{ prompt, chatJid, depth? }`                                                       |

`register_group` requires root. `delegate_group` authorized by
`isAuthorizedRoutingTarget(sourceGroup, group)`; `depth` max 3.
`fromPrototype=true` copies the caller's `prototype/` into the new
child folder and spawns the group via gated (see specs/4/10-ipc.md
for the `SpawnGroup(parentFolder, childJID)` contract).

### Routes

| Action         | MCP | Input                                |
| -------------- | --- | ------------------------------------ |
| `get_routes`   | yes | `{ jid }`                            |
| `list_routes`  | yes | `{ folder? }`                        |
| `set_routes`   | yes | `{ jid, routes[] }`                  |
| `add_route`    | yes | `{ jid, type, seq, match?, target }` |
| `delete_route` | yes | `{ id }`                             |

`delete_route` and `set_routes` refuse to remove the caller's own
tier-0 default route (self-harm guard).

### Sidecars (not yet implemented)

| Action              | MCP | Input                                            |
| ------------------- | --- | ------------------------------------------------ |
| `configure_sidecar` | yes | `{ name, image, env?, allowedTools?, network? }` |
| `request_sidecar`   | yes | `{ name, image, env?, allowedTools? }`           |
| `stop_sidecar`      | yes | `{ name }`                                       |
| `list_sidecars`     | yes | --                                               |

`configure_sidecar` persists to `container_config`; takes effect next
spawn. `request_sidecar` starts immediately for current session.

## Routing rules

RoutingRule types: `command`, `prefix` (@/# shortcuts, seq -2/-1),
`pattern` (regex), `keyword` (case-insensitive substring),
`sender` (regex on sender JID), `default`.

Evaluation order by seq (lower first); convention: prefix at -2/-1,
command at 0, others positive, default last.

`target` is a group folder name.

## Authorization

- Root group can target any JID
- Non-root can only target their own
- Enforced in action handlers

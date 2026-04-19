---
status: shipped
---

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

| Action         | MCP | Input                     |
| -------------- | --- | ------------------------- |
| `list_routes`  | yes | `{ folder? }`             |
| `set_routes`   | yes | `{ routes[] }`            |
| `add_route`    | yes | `{ seq, match?, target }` |
| `delete_route` | yes | `{ id }`                  |

`delete_route` and `set_routes` refuse to remove the last route
whose target equals the caller's own folder (self-harm guard:
never let an agent disconnect itself from every inbound source).

## Routing rules

Routes are a flat list of rows with `(seq, match, target)`. `match`
is a space-separated list of `key=glob` pairs (`platform`, `room`,
`chat_jid`, `sender`, `verb`) with Go `path.Match` semantics. Empty
`match` matches everything (wildcard).

Evaluation order: ascending `seq`, first match wins. Convention:
`seq=0` for default per-room rows, positive `seq` for overrides,
high `seq` for catch-alls.

`target` is a folder path by default. Explicit `folder:` /
`daemon:` / `builtin:` prefixes disambiguate typed targets.

See `specs/1/F-group-routing.md` for the full vocabulary.

## Authorization

- Root group can target any JID
- Non-root can only target their own
- Enforced in action handlers

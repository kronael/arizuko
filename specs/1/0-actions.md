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

| Action              | MCP | Input                                                                                        |
| ------------------- | --- | -------------------------------------------------------------------------------------------- |
| `refresh_groups`    | yes | --                                                                                           |
| `register_group`    | yes | `{ jid, name, folder, trigger, requiresTrigger?, containerConfig?, parent?, routingRules? }` |
| `delegate_group`    | yes | `{ group, prompt, chatJid, depth? }`                                                         |
| `set_routing_rules` | yes | `{ folder, rules }`                                                                          |

`register_group` requires root. `delegate_group` authorized by
`isAuthorizedRoutingTarget(sourceGroup, group)`; `depth` max 3.

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

RoutingRule types: `command`, `pattern` (regex), `keyword`,
`sender` (regex on sender JID), `default`.

Evaluation order: command > pattern > keyword > sender > default.

`target` is a group folder name.

## Authorization

- Root group can target any JID
- Non-root can only target their own
- Enforced in action handlers

---
status: draft
---

## <!-- trimmed 2026-03-15: TS removed, rich facts only -->

## status: shipped

# Channel Actions — Dynamic Registration and Filtered Manifest

Each social channel registers outbound actions on connect. Gateway
filters manifest per group so agents only see usable tools.
Agent-runner becomes a generic proxy (~50 lines).

## Platform-Based Manifest Filtering

`getManifest(sourceGroup)` filters actions by:

1. **minTier/maxTier**: hide from agents above the action's max tier
2. **platforms**: show only if agent's group has ANY platform that
   supports the action

Actions without constraints are visible to all agents.

### Filter Table

| Action           | minTier | platforms          | Visible to         |
| ---------------- | ------- | ------------------ | ------------------ |
| `send_message`   | --      | --                 | all agents         |
| `delegate_group` | --      | --                 | all agents         |
| `register_group` | 1       | --                 | root, world        |
| `refresh_groups` | 0       | --                 | root only          |
| `inject_message` | 1       | --                 | root, world        |
| `post`           | --      | reddit,mastodon... | agents with any    |
| `ban`            | --      | reddit,discord,... | agents with any    |
| `set_flair`      | --      | reddit             | agents with reddit |
| `timeout`        | --      | discord,twitch,yt  | agents with any    |

If agent calls an action on unsupported platform, handler returns
runtime error.

## Client Registry Pattern

Channels register their platform client on `connect()`, unregister on
`disconnect()`. Social actions are registered once at startup and
dispatch to whichever clients are connected via a shared
`Map<Platform, PlatformClient>`.

## Generic Agent-Runner Proxy

Agent-runner fetches manifest on startup, registers MCP tools
dynamically from it. Two special cases remain:

- `list_tasks`: reads local file (no IPC round-trip)
- `schedule_task`: cron validation moved to gateway handler

Manifest fetched once at startup (tools don't change during container
lifetime). Retry: 3 attempts, 500ms backoff if gateway not ready.

## MCP Tool Naming

All actions are generic verbs (post, reply, ban, pin). Handler
switches on `platformFromJid(jid)`. Agent uses the JID it received;
gateway resolves platform and dispatches.

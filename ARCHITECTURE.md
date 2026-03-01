# Architecture

## Overview

Kanipi is a multitenant Claude agent gateway. It polls messaging
channels for new messages, routes them to containerized Claude
agents via docker, and streams responses back to users.

TypeScript (ESM, NodeNext), SQLite (better-sqlite3), Docker.

## Message Flow

```
Channel (telegram/whatsapp/discord)
  -> DB (store message + chat metadata)
  -> message loop (poll getNewMessages)
  -> trigger check (direct mode or @name mention)
  -> GroupQueue (per-group serialization)
  -> runContainerAgent (docker run)
  -> stream output back to channel
```

Vite dev server runs alongside the gateway for web apps built
by agents. Managed by the bash entrypoint (`kanipi`), not Node.

## Components

### index.ts

Main loop. Initializes channels, starts IPC watcher, scheduler,
and message poll loop. Routes incoming messages to GroupQueue.
Handles group registration and discovery across channels.

### config.ts

All config from `.env` in the working directory + env vars.
Exports typed constants. Channel enablement logic lives here:

- `TELEGRAM_BOT_TOKEN` present → telegram enabled
- `DISCORD_BOT_TOKEN` present → discord enabled
- `whatsappEnabled()` → checks `store/auth/creds.json` exists

### db.ts

SQLite database. Stores messages, registered groups, chat
metadata, sessions, and scheduled tasks. All access is
synchronous (better-sqlite3). Key functions: `storeMessage`,
`getNewMessages`, `getAllRegisteredGroups`, `setSession`.

### channels/

One file per channel. Each implements `Channel` interface:

- `telegram.ts` — grammy bot, polls via webhook or long-poll
- `whatsapp.ts` — baileys client, event-driven
- `discord.ts` — discord.js client, event-driven

Each channel stores incoming messages via `storeMessage` and
provides `sendMessage(jid, text)` for outbound delivery.

`telegram.ts` converts agent markdown to Telegram HTML via
`mdToHtml()` (bold, italic, inline code, pre blocks) and sets
`parse_mode: HTML` on all `sendMessage` calls. Typing indicator
is refreshed every 4s via `setInterval` (Telegram expires it
after ~5s). Stopped when the agent emits `status=success` in
its JSON output — not when the container exits. Container may
remain alive (idle_timeout) but the typing indicator clears
immediately on response completion.

### container-runner.ts

Spawns docker containers per agent invocation. Builds volume
mounts (group folder, state, web dir), writes prompt to stdin,
reads JSON output from stdout between sentinel markers.

Sentinel markers (`---NANOCLAW_OUTPUT_START---` /
`---NANOCLAW_OUTPUT_END---`) delimit structured output from
agent log noise.

Output shape: `{ status, result, newSessionId, error }`.

Also writes `groups.json` and `tasks.json` snapshots into the
group IPC directory before each agent run. Runs `chownRecursive`
on `WEB_DIR` before mounting so the agent (uid 1000) can write
web files. Agent-teams (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`)
are disabled in the `settings.json` seed.

### container-runtime.ts

Docker lifecycle management. Starts/stops containers, cleans up
orphaned containers on startup. Orphan detection filters by
`ancestor=containerImage` to catch subagent containers spawned
by agent-team runs. Provides `readonlyMountArgs` for constructing
docker volume flags.

### group-queue.ts

Per-group message queue. Ensures sequential agent invocations
per group (no concurrent runs for the same group). Pipes stdin
to running containers for multi-turn within a session.

### group-folder.ts

Resolves and validates group folder paths. Enforces path
containment (no `..` escapes). Validates folder name format
before use in volume mounts or IPC paths.

### router.ts

Message formatting and outbound routing. Formats batched
messages as XML for agent prompt input. Strips `<internal>`
tags from agent output before sending to channel users.

### ipc.ts

File-based IPC between gateway and agent containers. Agents
write commands to a watched IPC directory; the watcher reads
and executes: send messages, register groups, create/update
tasks, sync group metadata.

### task-scheduler.ts

Cron-based scheduled task runner. Reads tasks from DB, fires
agent invocations at scheduled times. Uses `cron-parser` for
expression evaluation.

### mount-security.ts

Validates additional volume mounts requested by agents against
an allowlist at `~/.config/nanoclaw/mount-allowlist.json`.
Allowlist stored outside project root to prevent tampering.

## Container Model

Each agent invocation runs in a fresh docker container:

```
docker run
  -v groups/<folder>:/workspace     # group files (rw)
  -v data/sessions/<id>:/session    # session state (rw)
  -v web/:/web                      # web output (rw)
  -v <additional>                   # allowlisted mounts (ro)
```

The container entrypoint (`container/agent-runner/`) reads the
prompt from stdin and writes JSON output to stdout. Agent skills
are mounted from `container/skills/`.

## State

- Registered groups → SQLite (`groups` table)
- Message history → SQLite (`messages` table)
- Sessions → SQLite (`sessions` table) + filesystem
- Scheduled tasks → SQLite (`tasks` table)
- WhatsApp auth → `store/auth/` (baileys format)

## External Systems

| System   | Library       | Role                      |
| -------- | ------------- | ------------------------- |
| Telegram | grammy        | message channel           |
| WhatsApp | baileys       | message channel           |
| Discord  | discord.js    | message channel           |
| Docker   | child_process | agent container runtime   |
| Claude   | claude-code   | agent (runs in container) |

## Repository Layout

```
src/              gateway source (TypeScript)
  channels/       telegram, whatsapp, discord
container/        agent container build
  agent-runner/   in-container entrypoint
  skills/         agent-side skills
template/         seed for new instances
  web/            vite web app template
  workspace/      mcporter config seed
sidecar/          MCP server binaries
specs/            versioned API/behavior specs
kanipi            bash entrypoint (create/run/group)
```

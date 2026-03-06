# Architecture

## Overview

Kanipi is a multitenant Claude agent gateway. It polls messaging
channels for new messages, routes them to containerized Claude
agents via docker, and streams responses back to users.

TypeScript (ESM, NodeNext), SQLite (better-sqlite3), Docker.

## Message Flow

```
Channel (telegram/whatsapp/discord/email)
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

All config from `.env` + env vars. Exports typed constants.
Channel enablement by token presence (telegram/discord),
auth dir (whatsapp), or `EMAIL_IMAP_HOST` (email).
Test helpers `_overrideConfig`/`_resetConfig` gated behind
`NODE_ENV=test`.

### db.ts

SQLite database. Stores messages, registered groups, chat
metadata, sessions, and scheduled tasks. All access is
synchronous (better-sqlite3). Key functions: `storeMessage`,
`getNewMessages`, `getAllRegisteredGroups`, `setSession`.

Tables: `messages`, `registered_groups`, `sessions`,
`system_messages`, `tasks`, `auth_users`, `auth_sessions`.

`system_messages` stores pending events per group; flushed as
XML before agent stdin.

### slink.ts

Web channel for `POST /pub/s/:token`. Rate limiting (anon/auth),
JWT verification (HMAC-SHA256), `media_url` attachments. Returns
`SlinkResponse` — HTTP wiring in `web-proxy.ts`.

### commands/

Pluggable command registry. Each command implements `CommandHandler`
(name, description, handle). Commands are registered at startup and
intercepted before messages reach the agent queue. `writeCommandsXml`
serializes the registry to each group's IPC directory so agents can
discover available commands. Built-in: `/new` (clear session), `/ping`,
`/chatid`.

### channels/

One file per channel. Each implements `Channel` interface:

- `telegram.ts` — grammy bot, polls via webhook or long-poll
- `whatsapp.ts` — baileys client, event-driven
- `discord.ts` — discord.js client, event-driven
- `email.ts` — IMAP IDLE + SMTP reply threading

Each channel stores incoming messages via `storeMessage` and
provides `sendMessage(jid, text)` for outbound delivery.

`telegram.ts` converts agent markdown to Telegram HTML via
`mdToHtml()`. Typing indicator refreshes every 4s (Telegram
expires at ~5s), stops on `status=success` output — not on
container exit.

### web-proxy.ts

HTTP server sitting in front of Vite. Handles:

- `POST /pub/s/:token` — delegates to `handleSlinkPost` (unauthenticated)
- `GET /pub/REDACTED.js` — serves the public slink client script (unauthenticated)
- `GET /_REDACTED/stream?group=<n>` — SSE stream; pushes agent responses to
  browser clients via `addSseListener`/`removeSseListener` in `channels/web.ts`
- `POST /_REDACTED/message` — receives messages from authenticated web UI
- Everything else — proxied to Vite; HTML responses have `/_REDACTED/REDACTED.js`
  injected before `</body>`

Auth boundary: `/pub/` and `/_REDACTED/` prefixes bypass basic auth.
All other paths require `REDACTED_USERS` credentials (if configured).

### mime.ts + mime-enricher.ts + mime-handlers/

Attachment pipeline. `mime.ts` downloads attachments in parallel,
saves to session dir, runs matching handlers, returns annotation
lines for the agent prompt. `mime-enricher.ts` runs enrichment at
storage time (decoupled from dispatch); `waitForEnrichments()`
blocks until all pending jobs complete so transcriptions are
present when the prompt is assembled.

Handlers: `voice.ts` (multi-pass whisper transcription per
`.whisper-language`), `video.ts` (audio track extraction),
`whisper.ts` (shared HTTP client to sidecar, 60s timeout).

### container-runner.ts

Spawns docker containers per agent invocation. Builds volume
mounts (group folder, state, web dir), writes prompt to stdin,
reads JSON output from stdout between sentinel markers.

Sentinel markers (`---NANOCLAW_OUTPUT_START---` /
`---NANOCLAW_OUTPUT_END---`) delimit structured output from
agent log noise.

Output shape: `{ status, result, newSessionId, error }`.

`_spawnProcess` is an exported `let` binding (default: `spawn`) that
tests replace to mock docker without a running daemon.

Also writes `groups.json`, `tasks.json`, and `action_manifest.json`
snapshots into the group IPC directory before each agent run. Runs `chownRecursive`
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

### action-registry.ts + actions/

Unified action system. Each action has name, Zod schema, handler,
and optional command/MCP flags. Registry is the single source of
truth — IPC dispatch, MCP tools, and commands all reference it.

Actions: `send_message`, `send_file`, `schedule_task`, `pause_task`,
`resume_task`, `cancel_task`, `refresh_groups`, `register_group`,
`reset_session`.

`getManifest()` serializes all MCP-exposed actions as JSON Schema
for agent-side tool discovery.

### ipc.ts

File-based IPC between gateway and agent containers. Two modes:

1. **Request-response** (new): agent writes to `requests/`,
   gateway dispatches through action registry, writes reply to
   `replies/`. Enables typed responses and tool discovery.
2. **Fire-and-forget** (legacy): agent writes to `messages/`
   or `tasks/`, gateway drains and executes. Kept for backwards
   compat during rollout.

File sends serialized per group via drain lock.

### ipc-compat.ts

Backwards compatibility shim. Exports `processTaskIpc` (moved from
`ipc.ts`) for legacy fire-and-forget task IPC during rollout.

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
  -v groups/<folder>:/workspace/group   # group files (rw)
  -v kanipi/:/workspace/self            # kanipi source, all groups (ro)
  -v share/:/workspace/share             # cross-group shared state (ro/rw)
  -v web/:/workspace/web                # web output (rw)
  -v data/sessions/<id>:/workspace/ipc  # IPC directory (rw)
  -v <additional>:/workspace/extra/...  # allowlisted mounts (ro)
```

Full workspace namespace: `self`, `group`, `share`, `web`, `ipc`,
`extra`. `/workspace/self` exposes the kanipi source and all group
folders (read-only) — replaces the old `/workspace/project` which
only mounted the main group.

The container entrypoint (`container/agent-runner/`) reads the
prompt from stdin and writes JSON output to stdout. The agent-side
MCP server (`ipc-mcp-stdio.ts`) exposes gateway actions as tools
via request-response IPC (writes to `requests/`, polls `replies/`).
Agent-written `mcpServers` entries in `settings.json` are merged
with the built-in server at spawn time.

System messages (new-session, new-day) are flushed from DB and
prepended as XML to the stdin payload before user messages.

**reset_session IPC**: agents can request a session reset via IPC
(`type:'reset_session'`). The gateway evicts the current session
and the next invocation starts fresh.

**Skills seeding**: on first spawn for a group, `container/skills/`
is seeded to `~/.claude/skills/` inside the container. Includes
kanipi-specific skills plus development skills bundled from
REDACTED/tools (bash, go, python, typescript, etc.). A `CLAUDE.md`
is also seeded alongside.

**character.json**: agent identity is defined in
`container/character.json` (ElizaOS-style: bio, topics, adjectives,
style, messageExamples). Fields are randomized per query at runtime
to vary personality. Per-instance overrides via
`/workspace/share/character.json` are merged at load time.
Replaces the old `SOUL.md` approach.

**Migration system**: `container/skills/self/MIGRATION_VERSION`
tracks the applied version number. `container/skills/self/migrations/`
contains numbered migration files (`NNN-desc.md`). The `/migrate`
skill syncs all groups from the canonical source when the version
changes.

**Signal-driven IPC**: gateway writes IPC file then sends SIGUSR1
to the container; agent wakes immediately on signal rather than
waiting for the 500ms poll interval.

**Progress updates**: every 100 SDK messages, the agent runner
emits partial output to the channel.

**`error_max_turns` recovery**: resumes with `maxTurns=3`, asks
Claude to summarise progress, prompts user to say "continue".

## State

- Registered groups → SQLite (`registered_groups` table, includes `slink_token`)
- Message history → SQLite (`messages` table)
- Sessions → SQLite (`sessions` table) + filesystem. On agent error, the DB
  pointer is evicted so the next run starts a fresh session; JSONL remains on
  disk for history.
- System messages → SQLite (`system_messages` table), flushed per invocation
- Scheduled tasks → SQLite (`tasks` table)
- Web auth users → SQLite (`auth_users` table)
- Web auth sessions → SQLite (`auth_sessions` table)
- WhatsApp auth → `store/auth/` (baileys format)

## External Systems

| System   | Library       | Role                                               |
| -------- | ------------- | -------------------------------------------------- |
| Telegram | grammy        | message channel                                    |
| WhatsApp | baileys       | message channel                                    |
| Discord  | discord.js    | message channel                                    |
| Email    | IMAP/SMTP     | message channel (IDLE + reply threading)           |
| Docker   | child_process | agent container runtime                            |
| Claude   | claude-code   | agent (runs in container)                          |
| Whisper  | fetch (HTTP)  | voice/video transcription (kanipi-whisper sidecar) |

## Repository Layout

```
src/              gateway source (TypeScript)
  actions/        action handlers by domain (messaging, tasks, groups, session)
  channels/       telegram, whatsapp, discord, email
  commands/       slash command handlers (/new, /ping, /chatid)
container/        agent container build (make image → kanipi-agent)
  agent-runner/   in-container entrypoint
  skills/         agent-side skills
template/         seed for new instances
  web/            vite web app template
  workspace/      mcporter config seed
sidecar/          MCP server binaries
  whisper/        whisper sidecar (make image → kanipi-whisper)
specs/            versioned API/behavior specs
kanipi            bash entrypoint (create/run/group/vite)
```

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

### slink.ts

Web channel handler for public HTTP endpoints. Handles `POST /pub/s/:token`
requests: rate limiting (per slink token for anon, per JWT sub for
authenticated), JWT verification (HMAC-SHA256 when `AUTH_SECRET` is set),
`media_url` attachment handling. Returns a `SlinkResponse` without knowing
about HTTP — the HTTP wiring lives in `web-proxy.ts`.

Rate buckets are in-memory maps, reset per process. Anon: 10 rpm (per
token), auth: 60 rpm (per sub). Both configurable via `SLINK_ANON_RPM` /
`SLINK_AUTH_RPM`.

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

### mime.ts + mime-handlers/

Attachment pipeline. `mime.ts` defines shared types (`RawAttachment`,
`Attachment`, `AttachmentHandler`) and the `processAttachments` function:

1. Download all attachments in parallel (channel-specific downloaders)
2. Save each to a per-message directory in the session dir
3. Run matching `AttachmentHandler` to produce annotation lines
4. Return lines for inclusion in the agent prompt

Handlers in `mime-handlers/`:

- `voice.ts` — handles voice messages; calls whisper for transcription
- `video.ts` — handles video attachments; calls whisper for audio track
- `whisper.ts` — shared whisper HTTP client (`POST /inference` to sidecar)

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
  -v groups/<folder>:/workspace/group   # group files (rw)
  -v kanipi/:/workspace/self            # kanipi source, all groups (ro)
  -v global/:/workspace/global          # cross-group shared state (ro)
  -v web/:/workspace/web                # web output (rw)
  -v data/sessions/<id>:/workspace/ipc  # IPC directory (rw)
  -v <additional>:/workspace/extra/...  # allowlisted mounts (ro)
```

Full workspace namespace: `self`, `group`, `global`, `web`, `ipc`,
`extra`. `/workspace/self` exposes the kanipi source and all group
folders (read-only) — replaces the old `/workspace/project` which
only mounted the main group.

The container entrypoint (`container/agent-runner/`) reads the
prompt from stdin and writes JSON output to stdout.

**Skills seeding**: on first spawn for a group, `container/skills/`
is seeded to `~/.claude/skills/` inside the container. Includes
kanipi-specific skills plus development skills bundled from
REDACTED/tools (bash, go, python, typescript, etc.). A `CLAUDE.md`
is also seeded alongside.

**character.json**: agent identity is defined in
`container/character.json` (ElizaOS-style: bio, topics, adjectives,
style, messageExamples). Fields are randomized per query at runtime
to vary personality. Per-instance overrides via
`/workspace/global/character.json` are merged at load time.
Replaces the old `SOUL.md` approach.

**Migration system**: `container/skills/self/MIGRATION_VERSION`
tracks the applied version number. `container/skills/self/migrations/`
contains numbered migration files (`NNN-desc.md`). The `/migrate`
skill syncs all groups from the canonical source when the version
changes.

**Signal-driven IPC**: gateway writes IPC file then sends SIGUSR1
to the container; agent wakes immediately on signal rather than
waiting for the 500ms poll interval.

## State

- Registered groups → SQLite (`registered_groups` table, includes `slink_token`)
- Message history → SQLite (`messages` table)
- Sessions → SQLite (`sessions` table) + filesystem
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
| Docker   | child_process | agent container runtime                            |
| Claude   | claude-code   | agent (runs in container)                          |
| Whisper  | fetch (HTTP)  | voice/video transcription (kanipi-whisper sidecar) |

## Repository Layout

```
src/              gateway source (TypeScript)
  channels/       telegram, whatsapp, discord
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

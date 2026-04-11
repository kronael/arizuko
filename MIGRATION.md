# kanipi → arizuko Migration Guide

This document covers only non-obvious differences that cannot be inferred from
reading the arizuko README or code. If you see no entry for something, assume
the semantics are unchanged.

---

## IPC Transport: Files → Unix Socket + MCP

**Breaking.** kanipi used a file-based request/reply protocol:
`data/ipc/<group>/requests/<id>.json` → `data/ipc/<group>/replies/<id>.json`.
The agent wrote JSON files; the gateway polled/inotify-drained them.

arizuko replaces this with MCP over a unix socket at
`ipc/<group>/gated.sock`. Inside the container the socket is mounted at
`/workspace/ipc/gated.sock`. The agent connects to it via a socat bridge that
is injected into `.claude/settings.json` as an MCP server named `arizuko`.

**What this means for agents:**

- Agents no longer read/write files to call gateway actions.
- All IPC is now MCP tool calls — no migration path for file-based agents.
- The `list_actions` request type (kanipi-specific envelope) is gone; the MCP
  manifest lists available tools filtered by grants.

**What this means for skills:**

- Any skill that wrote to `requests/` or polled `replies/` must be rewritten
  to use MCP tool calls.
- The skill environment variable `GATEWAY_SOCK` or equivalent does not exist;
  the socket is wired automatically via socat in `.claude/settings.json`.

---

## IPC Action Differences

### Removed

- `refresh_groups` — no equivalent in arizuko. Groups are registered by channel
  adapters via the HTTP API; there is no in-band sync mechanism.
- `send_reply` — removed from the MCP manifest. Use `send_message` with the
  `chatJid` of the original chat.

### Changed: `send_file` path translation

kanipi translated `~/` to `/home/node/` inside the container.
arizuko translates paths relative to `/workspace/group/` — the group folder
mount point. A path like `~/tmp/out.pdf` in kanipi becomes a path under
`/workspace/group/` in arizuko. Update any skill that constructs `send_file`
paths.

### Changed: `schedule_task` fields

kanipi stored `group_folder` + `schedule_type` + `schedule_value` + optional
`context_mode`. arizuko stores `owner` (folder) + `cron` (single cron
expression, empty for one-time) + no `context_mode`. One-time tasks have an
empty `cron` field and `next_run` set at creation time by the scheduler.
`task_run_logs` table does not exist in arizuko.

### Changed: `get_grants` / `set_grants` availability

In kanipi these actions were available to any group that had the grant in its
manifest. In arizuko they are only injected into the MCP manifest for groups at
tier ≤ 1 (root and first-level children) — no grants check required, it is a
hard tier gate.

### Unchanged (all shipped as MCP tools)

`send_message`, `send_file`, `reset_session`, `inject_message`,
`register_group`, `escalate_group`, `delegate_group`, `get_routes`,
`set_routes`, `add_route`, `delete_route`, `schedule_task`, `pause_task`,
`resume_task`, `cancel_task`, `list_tasks`, `get_grants`, `set_grants`.

---

## Root Group Definition

kanipi: root = `folder === 'root'` (exact string match).
arizuko: root = any folder with no `/` in the name (tier 0).

If your instance uses a root folder named something other than `root` (e.g.
`main`, `boss`), kanipi treated it as tier 1. arizuko treats it as tier 0
(root privileges). Audit root-level grants accordingly.

---

## Configuration Changes

### New in arizuko (no kanipi equivalent)

| Var                                         | Purpose                                                                                                                                                     |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `API_PORT`                                  | Port for the channel registration HTTP API (default 8080). In kanipi this was the web server.                                                               |
| `CHANNEL_SECRET`                            | Shared secret that channel adapters must send as `Bearer` token to `/v1/channels/register`. No equivalent in kanipi — all channel adapters were in-process. |
| `AUTH_BASE_URL`                             | Explicit base URL for OAuth redirect URIs (required for OAuth). kanipi derived this from `WEB_HOST`; arizuko requires it explicitly.                        |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Google OAuth (`auth/oauth.go`, v1.5.0). Set both to enable the Google login button. No equivalent in kanipi.                                                |

### Removed from arizuko

| Var                                                        | Notes                                                                                                                                     |
| ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `ASSISTANT_HAS_OWN_NUMBER`                                 | WhatsApp-specific, removed with monolithic channel model.                                                                                 |
| `VITE_PORT`                                                | Accepted as alias for `WEB_PORT` in kanipi. arizuko only reads `WEB_PORT`; use `VITE_PORT_INTERNAL` for the internal Vite port.           |
| `SLINK_ANON_RPM` / `SLINK_AUTH_RPM`                        | Slink rate limits were per-instance vars. In arizuko slink tokens are stored on groups but slink rate limiting is not yet ported (dashd). |
| `WEB_PUBLIC`                                               | Replaced by `/pub/` path convention in proxyd. No env var needed.                                                                         |
| `WEBDAV_ENABLED` / `WEBDAV_URL`                            | Replaced by `DAV_ADDR` — proxyd proxies `/dav/` to a dufs container.                                                                      |
| `FILE_TRANSFER_ENABLED` / `FILE_DENY_GLOBS` / `FILE_MAX_*` | File command surface not ported to arizuko.                                                                                               |
| Social channel vars (`TWITTER_*`, `FACEBOOK_*`)            | Twitter and Facebook not ported. Mastodon, Bluesky, Reddit are separate Go daemons with own env vars (see service TOMLs).                 |

### Behaviorally different

- `ONBOARDING_ENABLED`: kanipi accepted `'1'`; arizuko accepts `'true'`.
  Change `ONBOARDING_ENABLED=1` to `ONBOARDING_ENABLED=true`.
- `CONTAINER_TIMEOUT`: kanipi treated the value as milliseconds (default
  1800000). arizuko still treats it as milliseconds but the env var is the
  same. Verify your value — the Go parser uses `strconv.Atoi` so the unit
  is identical.

---

## Channel Adapter Protocol

In kanipi, Telegram, Discord, WhatsApp were in-process channel implementations.
In arizuko they are separate processes that register via HTTP.

**Registration** (`POST /v1/channels/register`):

```json
{
  "name": "telegram",
  "url": "http://teled:8081",
  "jid_prefixes": ["telegram:"],
  "capabilities": { "send_text": true, "send_file": true, "typing": true }
}
```

Returns `{"ok": true, "token": "<per-session-token>"}`. The token rotates on
every re-registration.

**Inbound messages** (`POST /v1/messages`, `Authorization: Bearer <token>`):

```json
{
  "id": "...",
  "chat_jid": "telegram:12345",
  "sender": "telegram:99999",
  "sender_name": "Alice",
  "content": "hello",
  "timestamp": 1710000000,
  "reply_to": "",
  "attachments": []
}
```

`id` is optional — gateway generates one if absent. The router stamps
`messages.source` with the registered adapter name on each delivery.

**Outbound messages** (gateway calls adapter): `POST <url>/send`

```json
{ "chat_jid": "telegram:12345", "content": "hello", "format": "markdown" }
```

Authorization: `Bearer <CHANNEL_SECRET>` (the global secret, not the
per-channel token).

**Outbound files**: `POST <url>/send-file` as `multipart/form-data` with
fields `chat_jid`, `filename`, and file part named `file`.

**Health**: `GET <url>/health` must return HTTP 200. Consecutive failures cause
channel circuit-breaker.

**Typing**: `POST <url>/typing` with `{"chat_jid": "...", "on": true}`.

**Deregistration**: `POST /v1/channels/deregister`, `Authorization: Bearer
<per-channel-token>`.

---

## SQLite Schema Migration

The databases are not compatible. You must migrate data manually. Key
structural differences:

### `messages` table

- kanipi: composite PK `(id, chat_jid)` + FK to chats.
- arizuko: PK on `id` alone, no FK.
- arizuko adds `reply_to_id TEXT` (migration 0003), `source TEXT`
  (migration 0005, repurposed in 0023 as the canonical adapter-of-record
  per message), `topic TEXT` (migration 0008). The unused
  `group_folder TEXT` column was dropped in migration 0023.

### `chats` table

- kanipi: `(jid, name, channel, is_group, errored, agent_cursor)`.
- arizuko (post-0023): `(jid, errored, agent_cursor, sticky_group,
sticky_topic)` — all chat metadata (`name`, `channel`, `is_group`,
  `last_message_time`) was dropped. Receive identity moved to
  `messages.source`.

### `onboarding` table

- kanipi: stored `sender` and `world_name`.
- arizuko (post-0023): just `(jid, status, prompted_at)` — sender/world
  context is recoverable from `messages` joined on `jid`.

### `groups`

- Both kanipi and arizuko now use the table name `groups`.
- kanipi: renamed from `registered_groups` → `groups` (migration 0004).
  arizuko: renamed from `registered_groups` → `groups` (migration 0020),
  rekeyed by `folder` (PK) instead of `jid`. JID→folder mappings moved to
  `routes` table as `type='default'` entries.
- kanipi `groups` has: `max_children`, `world`. arizuko `groups` has:
  `spawn_ttl_days`, `archive_closed_days`, `state`; `max_children` lives
  in `container_config` JSON blob; no `world` column.
- arizuko `agent_cursor` moved from groups to `chats` table (migration 0020).

### `scheduled_tasks`

- kanipi: `group_folder`, `schedule_type`, `schedule_value`, `context_mode`,
  `last_run`, `last_result`.
- arizuko: `owner` (= group folder), `cron`, no `schedule_type`/`schedule_value`/
  `context_mode`/`last_run`/`last_result`.
- kanipi `task_run_logs` table has no equivalent in arizuko.

### `sessions`

- Both: after topic-session migration, PK is `(group_folder, topic)`. The
  migration SQL is compatible; existing rows get `topic=''`. This migration
  runs identically in both — no action needed if migrating data after this
  migration has run on both sides.

### `onboarding`

- kanipi: no `prompted_at` column (kanipi migration 0013).
- arizuko: has `prompted_at TEXT` column (arizuko migration 0009).

### `grants` / `grant_rules`

- kanipi: table is `grants` with `(folder, rules)`.
- arizuko: table is `grant_rules` with `(folder, rules)`.
- Data format (JSON string array) is identical; rename the table.

### New in arizuko only

- `channels` table: persistent channel adapter registry (migration 0009).
- `outbound_log` table (migration 0005): audit log for outbound messages.

---

## Auth / Session

Both use the same two-token pattern (short-lived JWT in `localStorage` +
30-day `refresh_token` httpOnly cookie named `refresh_token`).

**JWT claims are identical:** `{sub, name, exp, iat}`, HS256.

**Password hashing changed:** kanipi used bcrypt. arizuko uses argon2id
(`$argon2id$v=19$m=65536,t=3,p=4$...`). Existing bcrypt hashes in
`auth_users.hash` are not verifiable by arizuko — users must reset passwords
or be re-created.

**OAuth providers:**

- GitHub: shipped in both. Callback URL: `/auth/github/callback`.
- Discord: shipped in both. Callback URL: `/auth/discord/callback`.
- Google: shipped in both. Callback URL: `/auth/google/callback`.
- Telegram widget: shipped in arizuko (`POST /auth/telegram`), not in kanipi.

**`AUTH_BASE_URL` required:** kanipi derived OAuth redirect URIs from
`WEB_HOST`. arizuko requires `AUTH_BASE_URL=https://yourdomain.com`
explicitly.

---

## Agent Container Paths

| Purpose           | kanipi               | arizuko                     |
| ----------------- | -------------------- | --------------------------- |
| Group working dir | `/home/node`         | `/workspace/group`          |
| Self/skills dir   | `/workspace/self`    | `/workspace/self`           |
| Share dir         | `/workspace/share`   | `/workspace/share`          |
| IPC dir           | `/workspace/ipc`     | `/workspace/ipc`            |
| Web dir           | `/workspace/web`     | `/workspace/web`            |
| MCP socket        | n/a (file-based IPC) | `/workspace/ipc/gated.sock` |

Skills or CLAUDE.md files that reference `/home/node` must be updated to
`/workspace/group`.

`send_file` path translation: kanipi stripped `/home/node/`; arizuko strips
`/workspace/group/`. If agents construct absolute paths for `send_file`, update
them.

---

## Agent Skill / Template Structure

kanipi seeded from `prototype/workspace/` into the container. arizuko seeds
from `template/workspace/`. The directory names changed but the seeding
mechanism is identical.

**`prototype/.claude/` seeding** (kanipi-only): kanipi copied
`prototype/.claude/` into the agent session as a baseline. arizuko does not
do this — skills live only under `/workspace/self`. Agents relying on
`prototype/.claude/` must move those files into the skills system.

**Prototype spawning (`create --from`)**: kanipi supported cloning a group
folder as a prototype for new groups. Not implemented in arizuko — groups
start from the standard template.

---

## Features Not Yet in arizuko

From `.kanipi-delta.md` and code inspection:

| Feature                              | Notes                                                            |
| ------------------------------------ | ---------------------------------------------------------------- |
| `refresh_groups` IPC action          | No equivalent; channel adapters sync state via HTTP registration |
| Twitter / Facebook channels          | Not ported (Bluesky, Mastodon, Reddit are now native Go daemons) |
| `prototype/.claude/` seed            | Skills seeding path changed; prototype concept removed           |
| File transfer commands               | `/file put\|get\|list` — not ported                              |
| Per-group web prefix / virtual hosts | Not implemented                                                  |
| Cross-channel preemption             | Not implemented                                                  |

---

## Data Directory Layout

Top-level layout changed: kanipi used `data/ipc/<group>/` for IPC sockets;
arizuko uses `ipc/<group>/` (no `data/` prefix). The `data/` directory no
longer exists. Config field `DataDir` was replaced with `IpcDir`.

Agent session state (`.claude/`) now lives inside the group folder at
`groups/<folder>/.claude/` — there is no separate `data/sessions/` tree.
The instance root convention changed: kanipi used `DATA_DIR` pointing at cwd;
arizuko names instances as `arizuko_<name>` under `/srv/data/` by convention
but still reads `DATA_DIR` from `.env`.

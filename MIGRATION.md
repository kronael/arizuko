# kanipi → arizuko Migration

Non-obvious differences only. Unlisted semantics are unchanged.

## IPC: Files → Unix Socket + MCP

Breaking. kanipi used `data/ipc/<group>/requests/<id>.json` →
`replies/<id>.json`. arizuko uses MCP over a unix socket at
`ipc/<group>/gated.sock`, mounted in-container at
`/workspace/ipc/gated.sock`. Agent connects via socat bridge, wired into
`.claude/settings.json` as MCP server `arizuko`.

No migration path for file-based agents. Any skill writing `requests/` or
polling `replies/` must be rewritten as MCP tool calls. `GATEWAY_SOCK` env
does not exist — the socket is wired automatically.

## IPC Actions

**Removed**:

- `refresh_groups` — channel adapters sync via HTTP registration
- `send_reply` — use `send_message` with `chatJid`

**Changed**:

- `send_file`: kanipi stripped `/home/node/`; arizuko strips
  `/workspace/group/`. Update any skill that builds absolute paths.
- `schedule_task`: kanipi had `schedule_type`+`schedule_value`+`context_mode`;
  arizuko has `owner` + `cron` (empty = one-shot, `next_run` set at
  creation). `task_run_logs` table does not exist.
- Grants: kanipi exposed `get_grants` / `set_grants` MCP tools backed by
  per-folder rule blobs. arizuko replaced both with a single unified ACL
  (`acl` + `acl_membership` tables); `list_acl(folder)` is the read tool,
  writes go through CLI / dashd. Tier ≤ 1 gates the inspection tool.

## Root Group

kanipi: root = literal `folder === 'root'`.
arizuko: root = any folder with no `/` (tier 0).

A root named `main` / `boss` was tier-1 in kanipi, tier-0 in arizuko. Audit
root grants.

## Configuration

**New**:

| Var                                         | Purpose                                                  |
| ------------------------------------------- | -------------------------------------------------------- |
| `API_PORT`                                  | Channel registration API (default 8080)                  |
| `CHANNEL_SECRET`                            | Bearer token for `/v1/channels/register`                 |
| `AUTH_BASE_URL`                             | Explicit OAuth redirect base (was derived from WEB_HOST) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Google OAuth                                             |

**Removed**:

| Var                                                        | Notes                                                   |
| ---------------------------------------------------------- | ------------------------------------------------------- |
| `ASSISTANT_HAS_OWN_NUMBER`                                 | WhatsApp-specific, dropped                              |
| `VITE_PORT`                                                | Use `WEB_PORT`; `VITE_PORT_INTERNAL` for internal       |
| `WEB_PUBLIC`                                               | Replaced by `/pub/` convention in proxyd                |
| `WEBDAV_ENABLED` / `WEBDAV_URL`                            | Replaced by `DAV_ADDR` → dufs container                 |
| `FILE_TRANSFER_ENABLED` / `FILE_DENY_GLOBS` / `FILE_MAX_*` | File command surface not ported                         |
| `TWITTER_*`, `FACEBOOK_*`                                  | Not ported (mastd/bskyd/reditd are separate Go daemons) |

**Behavior**:

- `ONBOARDING_ENABLED`: kanipi accepted `'1'`; arizuko requires `'true'`.
- `CONTAINER_TIMEOUT`: milliseconds in both (Go `strconv.Atoi`).

## Channel Adapter Protocol

kanipi channels were in-process; arizuko adapters are separate HTTP processes.

**Register** (`POST /v1/channels/register`):

```json
{
  "name": "telegram",
  "url": "http://teled:8081",
  "jid_prefixes": ["telegram:"],
  "capabilities": { "send_text": true, "send_file": true, "typing": true }
}
```

Returns `{"ok": true, "token": "..."}` — rotates on re-registration.

**Inbound** (`POST /v1/messages`, `Bearer <token>`):

```json
{
  "id": "...",
  "chat_jid": "telegram:12345",
  "sender": "telegram:99999",
  "content": "hello",
  "timestamp": 1710000000
}
```

`id` optional. Router stamps `messages.source` with the adapter name.

**Outbound** (`POST <url>/send`, `Bearer <CHANNEL_SECRET>`):

```json
{ "chat_jid": "telegram:12345", "content": "hello", "format": "markdown" }
```

Files: `POST <url>/send-file` multipart (`chat_jid`, `filename`, `file`).
Health: `GET <url>/health`. Typing: `POST /typing {"chat_jid":"...","on":true}`.
Deregister: `POST /v1/channels/deregister`, `Bearer <per-channel-token>`.

## SQLite Schema

Databases are not compatible. Key structural differences:

- **`messages`**: kanipi PK `(id, chat_jid)` + FK. arizuko PK `id` alone, no
  FK. Added `reply_to_id`, `source` (adapter-of-record), `topic`. Dropped
  unused `group_folder`.
- **`chats`** (post-0023): `(jid, errored, agent_cursor, sticky_group,
sticky_topic)`. Dropped `name`, `channel`, `is_group`, `last_message_time`
  — receive identity moved to `messages.source`.
- **`onboarding`** (post-0023): `(jid, status, prompted_at)`. Sender/world
  recoverable via `messages` JOIN.
- **`groups`**: rekeyed by `folder` (PK) instead of `jid`; JID→folder
  mappings moved to `routes` as `type='default'`. kanipi had `max_children`,
  `world`; arizuko stores `max_children` in `container_config` JSON.
  `agent_cursor` moved from groups to `chats`.
- **`scheduled_tasks`**: kanipi `group_folder`+`schedule_type`+
  `schedule_value`+`context_mode`+`last_run`+`last_result`. arizuko `owner`
  - `cron`. No `task_run_logs`.
- **`sessions`**: both have PK `(group_folder, topic)` post-migration.
- **Grants**: kanipi `grants` table → arizuko unified `acl` +
  `acl_membership` (post-v0.38.0); legacy `grant_rules` / `user_groups`
  / `user_jids` tables dropped in migration 0053.
- **New in arizuko**: `channels` (persistent adapter registry),
  `outbound_log` (audit).

## Auth

Same two-token pattern (JWT + 30-day `refresh_token` httpOnly cookie).
JWT claims identical: `{sub, name, exp, iat}`, HS256.

**Password hashing changed**: kanipi bcrypt → arizuko argon2id
(`$argon2id$v=19$m=65536,t=3,p=4$...`). Existing hashes do not verify —
users must reset.

**OAuth**: GitHub, Discord, Google in both (callback `/auth/<provider>/callback`).
Telegram widget (`POST /auth/telegram`) only in arizuko.

**`AUTH_BASE_URL` required** — kanipi derived from `WEB_HOST`.

## Container Paths

| Purpose     | kanipi             | arizuko                     |
| ----------- | ------------------ | --------------------------- |
| Group dir   | `/home/node`       | `/workspace/group`          |
| Self/skills | `/workspace/self`  | `/workspace/self`           |
| Share       | `/workspace/share` | `/workspace/share`          |
| IPC         | `/workspace/ipc`   | `/workspace/ipc`            |
| Web         | `/workspace/web`   | `/workspace/web`            |
| MCP socket  | n/a                | `/workspace/ipc/gated.sock` |

Update skills/CLAUDE.md referencing `/home/node` → `/workspace/group`.
`send_file` path translation strips `/workspace/group/` (not `/home/node/`).

## Skills / Templates

kanipi seeded from `prototype/workspace/`; arizuko from `template/workspace/`.
Name changed; mechanism identical.

`prototype/.claude/` seeding: kanipi only. Skills now live under
`/workspace/self`. Move any baseline files into the skills system.

Prototype spawning is wired via `ONBOARDING_PROTOTYPE` env (operator
default) and `register_group fromPrototype=true` (per-call). New groups
clone from `groups/<prototype>/` instead of bare `template/`.

## Features Not Ported

| Feature                     | Notes                                     |
| --------------------------- | ----------------------------------------- |
| `refresh_groups` IPC        | Adapters sync via HTTP registration       |
| Twitter / Facebook channels | mastd/bskyd/reditd/twitd separate daemons |
| File transfer (`/file ...`) | Not ported                                |
| Cross-channel preemption    | Not implemented                           |

Previously listed as not-ported, now shipped: prototype spawning (see
above), per-group web prefix / vhosts (proxyd `vhosts.json` hot-reload).

## Data Directory

kanipi: `data/ipc/<group>/`. arizuko: `ipc/<group>/` (no `data/` prefix);
`data/` directory removed; `DataDir` config → `IpcDir`.

Agent session state now lives at `groups/<folder>/.claude/` (no separate
`data/sessions/` tree). Instances live at `/srv/data/arizuko_<name>/`;
`DATA_DIR` still read from `.env`.

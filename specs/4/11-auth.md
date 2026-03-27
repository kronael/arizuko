---
status: draft
---

# auth

**Status**: shipped — `auth/` package (identity.go, policy.go, web.go, jwt.go, oauth.go, middleware.go)

Authorization policy engine. Consumers call it to check
whether a caller is allowed to perform an action.

## Role

auth is a pure policy engine. It answers one question:

> Can caller with identity X perform action Y on target Z?

It doesn't know what actions do. It doesn't execute anything.
It receives a query and returns allow or deny.

## Interface

Called by ipc after resolving caller identity from
the socket path.

```
authorize(caller, action, target) → allow | deny
```

Where:

- `caller`: `{folder, tier}` — resolved by ipc from socket path
- `action`: tool name (e.g. `send_message`, `schedule_task`)
- `target`: action-specific (e.g. chat_jid, task_id)

## Policy rules

### Tier-based access

Most tools are gated by grant rules, not by tier directly.
ipc only enforces tier for two tool groups:

| Tier check | Tools                      |
| ---------- | -------------------------- |
| tier ≤ 1   | `get_grants`, `set_grants` |
| tier ≤ 2   | `refresh_groups`           |

All other tools (`send_message`, `send_reply`, `send_file`,
`inject_message`, `register_group`, `delegate_group`,
`escalate_group`, `reset_session`, `get_routes`, `set_routes`,
`add_route`, `delete_route`, `schedule_task`, `pause_task`,
`resume_task`, `cancel_task`, `list_tasks`) are controlled
by grant rules only — no tier check at the ipc level.
auth.Authorize is still called for ownership checks on
specific tools (see ownership checks below).

Tier 0 (root) can call everything permitted by grants.

### Ownership checks

Some actions require ownership validation beyond tier:

- `pause_task` / `resume_task` / `cancel_task`: caller's
  folder must match task's `owner` (tier 2), or be in same
  world (tier 1), or be root (tier 0)
- `set_routes` / `add_route` / `delete_route`: tier 1 can
  only modify routes targeting own subtree
- `delegate_group`: target must be a child of caller's folder

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

Migration service name: `auth`.

These tables are for web authentication, separate from
the tier-based MCP authorization which is computed from
folder depth (no tables needed).

## Web Authentication

<!-- source: arizuko specs/3/A-auth.md, synced 2026-03-15 -->

Web auth is handled by the same auth package. Separate from
MCP tier-based authorization — this covers human user login.

### What ships

- local username/password login (argon2 hashed)
- JWT access token minting (HMAC-SHA256, 1h TTL)
- refresh-token sessions in DB
- login/refresh/logout routes
- GitHub OAuth (code exchange + user info)
- Discord OAuth (code exchange + user info)
- Telegram Login Widget verification
- user management CLI (`arizuko config <instance> user {add|rm|list|passwd}`)
- login page with OAuth buttons (shown when providers configured)

### Routes

```text
GET  /auth/login              login page (shows OAuth buttons if configured)
POST /auth/login              local username/password login
POST /auth/refresh            rotate refresh token, mint new JWT
POST /auth/logout             clear session

GET  /auth/github             redirect to GitHub authorize
GET  /auth/github/callback    exchange code, create session, redirect /
GET  /auth/discord            redirect to Discord authorize
GET  /auth/discord/callback   exchange code, create session, redirect /
POST /auth/telegram           verify widget hash, create session
```

### Token model

- Access token: HMAC-SHA256 JWT, 1h TTL, returned in JSON
- Refresh token: opaque random token, SHA-256 hash stored in `auth_sessions`
- Refresh rotation: old refresh token deleted, new one inserted

### Cookie behavior

```text
Set-Cookie: refresh=<token>; HttpOnly; Path=/; Max-Age=2592000; SameSite=Strict
```

OAuth callbacks use `SameSite=Lax` to survive the redirect from the provider.

### DB tables

```sql
CREATE TABLE auth_users (
  id INTEGER PRIMARY KEY,
  sub TEXT UNIQUE NOT NULL,
  username TEXT UNIQUE NOT NULL,
  hash TEXT NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_sub TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### OAuth providers

**GitHub**: Env vars `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`.
Flow: `/auth/github` redirects to GitHub with state cookie. Callback
exchanges code for access token, fetches `/user`, creates local
`auth_users` entry with `sub=github:<id>`, `username=gh_<login>`.

**Discord**: Env vars `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET`.
Flow: `/auth/discord` redirects to Discord with state cookie. Callback
exchanges code for access token, fetches `/users/@me`, creates local
`auth_users` entry with `sub=discord:<id>`, `username=dc_<username>`.

**Telegram**: Uses `TELEGRAM_BOT_TOKEN` to verify the Login Widget hash
(HMAC-SHA256). POST `/auth/telegram` with widget data JSON. Server verifies
`auth_date` is within 24h, computes HMAC, creates local `auth_users`
entry with `sub=telegram:<id>`, `username=tg_<username>`.

### User management CLI

```bash
arizuko config <instance> user list
arizuko config <instance> user add <username> <password>
arizuko config <instance> user rm <username>
arizuko config <instance> user passwd <username> <password>
```

`user add` hashes with argon2, inserts with `sub=local:<uuid>`.
`user rm` deletes user and all their sessions.

### Auth mode

- `AUTH_SECRET` set: session cookie auth required for non-public routes
- `WEB_PUBLIC=1`: no auth, everything accessible
- Neither: no auth required (open access)

## Layout

```
auth/
  identity.go
  policy.go
  web.go
  jwt.go
  oauth.go
  middleware.go
```

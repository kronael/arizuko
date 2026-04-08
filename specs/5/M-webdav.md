---
status: in-progress
---

# WebDAV Workspace Access

Expose each group's workspace directory over WebDAV so users can browse,
upload, and manage files directly — same files the agent delivers via
`send_file` in chat.

## Design decision: dufs over Caddy

Original spec called for Caddy + `mholt/caddy-webdav` module. Replaced with
`dufs` (single Rust binary): ships a built-in web UI, WebDAV, simpler
deployment (no custom Caddy build), same security model. Caddy's strict RFC
compliance (LOCK semantics) is not needed for the browser/rclone use case.

## Architecture

```
WebDAV client (Cyberduck, rclone, browser)
    ↓ Basic Auth: username:webdav_token
Gateway web-proxy (/dav/<group>/*)
    ↓ validates token (SHA-256 hash in auth_users.webdav_token_hash)
    ↓ checks group ACL (auth_users.webdav_groups JSON array)
    ↓ strips Authorization, rewrites path to /<group><rest>
dufs (localhost:8179, no auth)
    ↓ serves GROUPS_DIR root
/srv/data/<instance>/groups/<group>/
```

## Auth

Each `auth_users` row gains:

- `webdav_token_hash TEXT` — SHA-256 of a static token (shown once at generation)
- `webdav_groups TEXT` — JSON array of allowed group names (default `["root"]`)

Gateway validates Basic Auth before forwarding; dufs sees no credentials.

## URL scheme

```
https://<host>/dav/<group>/         # web UI + WebDAV root
https://<host>/dav/<group>/media/   # media subfolder
https://<host>/dav/<group>/logs/    # read-only (write methods blocked in proxy)
```

## Implementation

### DB migration

```sql
ALTER TABLE auth_users ADD COLUMN webdav_token_hash TEXT;
ALTER TABLE auth_users ADD COLUMN webdav_groups TEXT DEFAULT '["root"]';
```

### Config

```
WEBDAV_ENABLED=true
WEBDAV_URL=http://localhost:8179
```

### dufs process

Start alongside the gateway (in entrypoint or docker-compose), no auth:

```bash
dufs --bind 127.0.0.1 --port 8179 --allow-upload --allow-delete \
  --allow-create-dir --allow-move "$GROUPS_DIR"
```

Binary added to the agent container image or run as a sidecar — follow
whichever pattern kanipi ships (see `kanipi/specs/5/f-webdav.md`).

### web-proxy route

`ALL /dav/:group/*`:

1. Parse Basic Auth, look up `auth_users`, verify SHA-256 token hash
2. Check `webdav_groups` includes `:group`
3. Block write methods on `logs/` prefix and deny globs (`.env`, `**/*.pem`, `.git/**`)
4. Rewrite to `http://localhost:8179/<group><rest>` and proxy

## Security

- dufs binds `127.0.0.1` only — never exposed directly
- All auth in proxy layer before dufs sees the request
- WebDAV token separate from login password
- `logs/` read-only; sensitive files write-blocked in proxy

## Out of scope

- macOS Finder reliability (dufs LOCK is advisory; Finder may be unreliable)
- Multi-instance shared filesystem
- CalDAV / CardDAV

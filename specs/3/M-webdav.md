# WebDAV Workspace Access — open (v2)

Expose each group's workspace directory over WebDAV so users can mount
it in Finder, Cyberduck, or any WebDAV client and browse/edit files
directly — without going through the agent or Telegram file transfer.

## Reference

- `hive/agent/internal/agent/webdav.go` — Caddy basicauth setup inside each VM
- `hive/internal/server/handlers_proxy.go` — platform proxies `/api/vm/{id}/files`
  to `http://<vm-ip>:80/` (Caddy `file_server browse`)
- `crackbox/` — identical pattern

Hive/crackbox run Caddy inside each VM (`root * /`, `file_server browse`,
basicauth via bcrypt), then proxy through the platform API. Credentials
are provisioned per-VM and stored in the platform DB.

## Scope

Kanipi adaptation: one Caddy sidecar per kanipi instance (not per group),
serving `GROUPS_DIR` as root. Auth reuses the existing kanipi user accounts
(`auth.ts`). Access is proxied through the existing web layer.

## Architecture

```
WebDAV client (Finder / Cyberduck)
        │  Basic Auth: username:webdav_token
        ▼
  kanipi web proxy  (web-proxy.ts)
        │  path: /dav/<group>/
        │  validates webdav_token against DB
        ▼
  Caddy sidecar  (kanipi-webdav container)
        │  no-auth (trusted internal network only)
        │  root: GROUPS_DIR
        ▼
  /srv/data/kanipi_<name>/groups/<group>/
```

Caddy does the WebDAV heavy lifting (PROPFIND, PUT, MKCOL, COPY, MOVE,
DELETE, LOCK). Gateway proxy handles auth and path scoping.

## Auth

Uses the existing argon2id local-account system from `specs/1/3-auth.md`.

WebDAV clients require HTTP Basic Auth. Kanipi issues a **WebDAV token**
per user (separate from JWT — static tokens designed for client mounts):

```bash
kanipi config <instance> user webdav-token <username>   # generate/rotate
kanipi config <instance> user webdav-token <username> --show
```

Token stored as `webdav_token` on the `auth_users` row (SHA-256 stored,
cleartext shown once at generation). Gateway validates Basic Auth header:
`Authorization: Basic base64(username:webdav_token)`.

Slink auth (from `specs/1/W-slink.md`): slink Bearer tokens are not
accepted for WebDAV — slinks are inbound delivery webhooks, not user
sessions. WebDAV needs a real user account.

No token → 401. Valid token → proxy to Caddy with path scoped to the
allowed group(s).

### Group access control

Each auth user has a `webdav_groups` list (default: `["main"]`). Users
may only access groups they're authorised for. Paths outside their groups
return 403 without reaching Caddy.

```bash
kanipi config <instance> user webdav-groups <username> main support
```

## URL scheme

```
https://<host>/dav/<group>/           # directory listing
https://<host>/dav/<group>/media/     # media subfolder
https://<host>/dav/<group>/logs/      # conversation logs (read-only)
```

`logs/` is served read-only (Caddy `file_server` without write methods).
All other paths under `<group>/` are read-write.

### Client mount examples

**macOS Finder**: `⌘K` → `https://<host>/dav/main/` → enter username + webdav_token
**Cyberduck**: WebDAV (HTTPS), same URL and credentials
**rclone**: `rclone config` → WebDAV → URL `https://<host>/dav/main/`

## Caddy sidecar

Single `kanipi-webdav` container, no auth (internal only), full WebDAV:

```caddyfile
:8179 {
    root * /srv/groups
    webdav {
        prefix /
    }
    file_server browse
}
```

Requires Caddy with the `mholt/caddy-webdav` module (not in stock Caddy).
Built as a separate image: `sidecar/webdav/Dockerfile`.

Mount: `-v /srv/data/kanipi_<name>/groups:/srv/groups:rw`

Caddy does LOCK (advisory, in-memory) — sufficient for single-instance.
No NFS or distributed locking needed.

## Gateway changes

### `web-proxy.ts`

New route: `ALL /dav/:group/*`

1. Parse Basic Auth header.
2. Look up username in `auth_users`; verify webdav_token (SHA-256 compare).
3. Check `webdav_groups` includes `:group`.
4. Check `logs/` prefix → strip write methods (PUT/MKCOL/DELETE/COPY/MOVE/LOCK).
5. Rewrite to `http://webdav-sidecar:8179/<group>/<rest>`.
6. Proxy with `http-proxy` or `node-http-proxy` (same lib used elsewhere).

### `db.ts`

```sql
ALTER TABLE auth_users ADD COLUMN webdav_token_hash TEXT;
ALTER TABLE auth_users ADD COLUMN webdav_groups TEXT DEFAULT '["main"]';
```

### `config.ts`

```
WEBDAV_ENABLED=true          # disables /dav/* routes when false
WEBDAV_SIDECAR_URL=http://localhost:8179
```

## Ansible

Add `kanipi-webdav` service entry to `host_vars/REDACTED.../vars`:

```yaml
- image: kanipi-webdav
  name: kanipi_webdav
  params: >-
    -v /srv/data/REDACTED/groups:/srv/groups:rw
    -p 127.0.0.1:8179:8179
```

One sidecar per instance (separate port per instance if multiple).

## Security

- Caddy sidecar binds `127.0.0.1` only — never exposed directly.
- All auth happens in the gateway proxy before Caddy sees the request.
- WebDAV token is separate from the login password — compromise of one
  doesn't expose the other.
- `logs/` read-only: agent conversation logs visible but not modifiable.
- `deny_globs` from `specs/1/g-files.md` applied in the proxy layer for
  write methods (block `.env`, `**/*.pem`, `.git/**`).
- HTTPS required (Caddy in production sits behind nginx/Caddy TLS terminator).

## Out of scope

- Per-agent LOCK notifications (agent sees stale data if user edits
  a file mid-run — acceptable; agent re-reads files each invocation).
- Multi-instance shared filesystem (each instance mounts its own groups dir).
- CalDAV / CardDAV — pure WebDAV.

---
status: shipped
---

# WebDAV workspace

Per-group workspace exposed over WebDAV via `dufs` (single Rust
binary) bound to `127.0.0.1:8179`. proxyd validates Basic Auth against
`auth_users.webdav_token_hash` + `webdav_groups` JSON array, strips
Authorization, rewrites path to `/<group>/<rest>`.

URLs: `https://<host>/dav/<group>/[media|logs]/`. `logs/` read-only in
proxy. `.env`/`*.pem`/`.git/**` write-blocked.

Config: `WEBDAV_ENABLED`, `WEBDAV_URL`. Token hash generated once per
user.

Rationale: dufs over Caddy+mholt/caddy-webdav — ships UI, simpler
deploy, same security model.

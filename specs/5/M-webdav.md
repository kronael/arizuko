---
status: partial
---

# WebDAV workspace

Per-group workspace exposed over WebDAV via `dufs` (single Rust
binary). proxyd routes `/dav/<group>/<rest>` → `/<group>/<rest>` on
the dufs upstream, after validating the user's groups claim from the
JWT cookie / Bearer token.

URLs: `https://<host>/dav/<group>/...`.

Config: `WEBDAV_ENABLED` (default `true` since 2026-04-27),
`WEBDAV_URL` (auto-set to `http://davd:8080` by compose generation).

## What's shipped

- dufs container included in the compose output by default.
- proxyd `/dav/*` route, JWT-gated via `requireAuth`, group claim
  check against the path's `<group>` segment.
- Rewrite from `/dav/<group>/<rest>` to `/<group>/<rest>` before
  forwarding.
- proxyd `davAllow` middleware: blocks writes (PUT/POST/MKCOL/DELETE/
  MOVE/COPY/PROPPATCH) on `.env`, `*.pem`, `.git/**` segments; makes
  `<group>/logs/**` read-only (any non-read method → 403). Read methods
  (GET/HEAD/OPTIONS/PROPFIND) pass through unchanged.

## What was originally specced but is NOT shipped

- **Basic Auth + per-user `webdav_token_hash`**. The original spec
  proposed Basic Auth so that desktop WebDAV clients (Finder, Windows
  Explorer) could authenticate without browser cookies. As shipped,
  `/dav/*` uses the same `requireAuth` middleware as `/dash/*` — JWT
  cookie or Bearer header. Cookie-less clients need `/dash/profile`
  to issue a Bearer token they can configure their client with.
  Pick one direction when this gets focus:
  - add Basic Auth handler + `webdav_token_hash` column +
    `WEBDAV_GROUPS` JSON scope, OR
  - declare cookie/Bearer the canonical answer and remove the
    Basic-Auth language from this spec.
- (shipped) `logs/` read-only and `.env` / `*.pem` / `.git/**`
  write-block — see `davAllow` in `proxyd/main.go`.

Rationale: dufs over Caddy+mholt/caddy-webdav — ships UI, simpler
deploy, same security model.

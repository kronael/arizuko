---
status: shipped
shipped: 2026-05-01
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

- dufs container included in the compose output by default. davd's
  `/data` mount is read-write; write enforcement is in proxyd's
  `davAllow` guard, not at the Docker volume layer.
- proxyd `/dav/*` route, JWT-gated via `requireAuth`, group claim
  check against the path's `<group>` segment.
- `/dav` (bare, no group) redirects to the first non-`**` group in
  _sorted_ order — deterministic across requests.
- Rewrite from `/dav/<group>/<rest>` to `/<group>/<rest>` before
  forwarding.
- proxyd `davAllow` middleware: blocks writes (PUT/POST/MKCOL/DELETE/
  MOVE/COPY/PROPPATCH) on `.env`, `*.pem`, `.git/**` segments; makes
  `<group>/logs/**` read-only (any non-read method → 403). Read methods
  (GET/HEAD/OPTIONS/PROPFIND) pass through unchanged. Ordinary writes
  reach dufs and land on disk.

## Authentication — final shape

Cookie/Bearer is canonical. `/dav/*` uses the same `requireAuth`
middleware as `/dash/*`: JWT cookie (browser) or Bearer token
(WebDAV client). No Basic Auth, no per-user `webdav_token_hash`.

Password support comes for free via proxyd's local-OAuth provider:
users without a browser cookie run the local OAuth flow to exchange
a username/password for a Bearer token, then configure their WebDAV
client with that token. `/dash/profile` is the human-facing entry
point for issuing/revoking tokens.

Rationale: dufs over Caddy+mholt/caddy-webdav — ships UI, simpler
deploy, same security model.

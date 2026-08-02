---
status: shipped
---

# Browser SDK — `/assets/arizuko-client.js`

A vanilla-JS client for the round-handle protocol, plus the
shared-static-asset mechanism that hosts it. The protocol it speaks moved
from `/slink/<token>` to `/chat/<token>` — see
[`../5/W-webhook-routes.md`](../5/W-webhook-routes.md); the SDK and the
asset mechanism below are unchanged.

## Why ship a client at all

Every page that talks to a chat token re-implements the same four-call
dance: POST, `EventSource` on `/sse`, status poll on terminal
`round_done`, snapshot for backfill. The dance is small but it drifts —
one page lacks `Last-Event-Id` replay, another forgets to close the
source on `round_done`, a third hardcodes paths `/config` already
exposes. **One renderer, many sinks** says we ship the renderer once.

## Hosting decision — `embed.FS`, not `template/web/pub/`

webd serves `/assets/*` from an `embed.FS` baked into the binary
(`webd/assets.go`). Single source of truth, version-locked to the daemon,
so there is no copy-drift between the checked-in web tree and what a
running instance actually serves. Path traversal is structurally
impossible: the handler only reads keys present in the `embed.FS`, so
`..` or an unknown name 404s before touching the filesystem.

Response headers: `Cache-Control: public, max-age=3600` (bounded so a
deploy propagates without operator action), strong content-hash `ETag`,
`Access-Control-Allow-Origin: *`, and `X-Arizuko-Version` so a consumer
can pin.

## Versioning

The unversioned URL always points at the latest stable major. A breaking
change adds `/assets/arizuko-client-v2.js` alongside; the old file stays
at the old path. No npm package — operators self-host, and the canonical
URL is the arizuko-served one.

## Discovery

`GET /chat/<token>/config` carries `"sdk": "/assets/arizuko-client.js"`,
so a cross-origin page can fetch config first and `import()` the SDK
without knowing the operator's domain at build time.

## Out of scope

TypeScript `.d.ts`, WebSocket transport (SSE covers the round-handle
protocol), service-worker/offline, and an SDK-rendered chat UI — page
authors build their own; the inline `/chat/<token>/` page is the
reference implementation.

---
status: spec
---

# Slink SDK — `/assets/arizuko-client.js`

Extension of [Z-slink-widget.md](Z-slink-widget.md). That spec shipped the
REST surface (`/config`, JSON-default POST, CORS, `/chat` page). This one
ships a vanilla-JS client that consumes it, plus the shared-static-asset
mechanism that hosts it.

## Why

Every agent or third-party page that wants to talk to a slink today
re-implements the same five-call dance: `fetch` POST, `EventSource`
on `/sse`, status poll on terminal `round_done`, snapshot for backfill,
optional steer. The dance is small but it drifts — one page lacks
`Last-Event-Id` replay, another forgets to close the source on
`round_done`, a third hardcodes paths the `/config` endpoint already
exposes. **One renderer, many sinks** says we ship the renderer once.

## API surface

`Arizuko.connect(token, opts?) → Promise<Slink>` — fetches `/config`,
returns a `Slink` bound to the token. `opts.baseURL` overrides the
origin (default: same-origin).

```ts
interface Slink {
  token: string;
  folder: string;
  name: string;
  send(content: string, opts?: { topic?: string }): Promise<TurnHandle>;
  steer(turnId: string, content: string): Promise<TurnHandle>;
  status(turnId: string): Promise<StatusEnvelope>;
  snapshot(turnId: string, opts?: { after?: string }): Promise<Snapshot>;
  stream(turnId: string, handlers: StreamHandlers): () => void; // returns close-fn
}

interface TurnHandle {
  user: Message;
  turn_id: string;
  status: 'pending';
  chained_from?: string;
}
interface StreamHandlers {
  onMessage?: (frame) => void;
  onStatus?: (frame) => void;
  onDone?: (env) => void;
  onError?: (err) => void;
}
```

Implementation: vanilla JS, ≤ 250 LOC, no build step, no dependencies,
browser-only. `fetch` for POST/GET, `EventSource` for SSE, JSDoc
annotations so IDEs get autocomplete without a `.d.ts` file.

Out of scope (later, if needed): a `slink.chat({mountSelector})` UI
renderer. The first cut keeps the SDK protocol-only — UI is the page
author's job. Inline chat page (`/slink/<t>/chat`) stays as the
reference implementation; once the SDK is stable it migrates onto
it.

## Hosting — `/assets/arizuko-client.js`

webd serves `/assets/*` from an `embed.FS` baked into the binary at
build time. Single source of truth, version-locked to the daemon —
no copy-drift between `template/web/pub/` and runtime.

| Header                         | Value                                           |
| ------------------------------ | ----------------------------------------------- |
| `Content-Type`                 | by extension (`.js` → `application/javascript`) |
| `Cache-Control`                | `public, max-age=3600` (1h, conservative)       |
| `ETag`                         | strong, content-hash; supports `If-None-Match`  |
| `Access-Control-Allow-Origin`  | `*`                                             |
| `Access-Control-Allow-Methods` | `GET, OPTIONS`                                  |

Path-traversal is structurally impossible — the handler only reads
keys that exist in `embed.FS`, so any path with `..` or unknown name
falls through to 404 before touching the filesystem.

## Discovery — `/slink/<token>/config`

The config response (shipped in Z-slink-widget) gains one field:

```json
{
  "token": "...", "folder": "...", "name": "...",
  "endpoints": {...},
  "sdk": "/assets/arizuko-client.js"
}
```

Pages do:

```html
<script src="/assets/arizuko-client.js"></script>
<script>
  const slink = await Arizuko.connect("<token>");
  const turn = await slink.send("hello");
  slink.stream(turn.turn_id, {onMessage: m => render(m)});
</script>
```

The `sdk` hint lets advanced pages fetch `/config` first, discover
the SDK URL, and `import()` it dynamically — useful when the page is
cross-origin and the operator's domain is unknown at build time.

## Versioning

- Ship as `/assets/arizuko-client.js` (latest stable). Cache-Control
  bounded to 1h so versioned releases propagate within a deploy cycle
  without operator action.
- The binary version is exposed via `X-Arizuko-Version` header on the
  asset response, so consumers can pin if they want to.
- **Breaking changes** add `/assets/arizuko-client-v2.js` alongside;
  the old file stays at the old path. The unversioned URL always
  points at the latest stable major.
- A `.d.ts` file is out of scope for v1; ship if/when a downstream
  TS project requests it.

## Skill — `ant/skills/slink/SKILL.md`

A new agent skill (sibling to existing `slink-mcp/`) teaches agents
how to build branded chat pages on top of the SDK. Trigger phrases:
"branded chat page", "embed slink in my site", "host a chat widget."
Content:

- 20-line copy-pasteable HTML template that loads the SDK and renders
  a minimal chat thread.
- The five-method API table.
- Pointer to `/pub/examples/slink-sdk.html` (operator-facing reference
  page shipped under `template/web/pub/examples/`).

## Sample page — `template/web/pub/examples/slink-sdk.html`

Minimal working page (~50 lines) that uses the SDK to talk to a slink.
Operators copy this to `/workspace/web/pub/<app>/index.html`, swap the
token, and have a branded chat in under a minute.

## Future — docs hosting on the same mechanism

The `/assets/*` mechanism is generic. Later work can:

1. Add a `/docs/*` route in webd that serves a Swagger-UI bundle from
   the same `embed.FS` (referenced by `specs/6/4-openapi-discoverable`).
2. Migrate parts of `template/web/pub/` (CSS, JS) to the embedded FS
   so cold-start `arizuko create` ships fewer copy-prone files.

Neither is part of this spec. The mechanism is designed to absorb
them when the work happens.

## Out of scope

- TypeScript types — ship `.d.ts` later if asked.
- npm package — operators self-host; the canonical URL is the
  arizuko-served one.
- WebSocket transport — SSE covers the round-handle protocol fine.
- Service Worker / offline mode — page authors layer their own.
- SDK-rendered chat UI — page authors build their own; the inline
  `/chat` page is the reference.

## Acceptance criteria

1. `GET /assets/arizuko-client.js` → 200, `application/javascript`,
   `Access-Control-Allow-Origin: *`, non-empty body that parses as JS.
2. `GET /slink/<token>/config` JSON contains `"sdk": "/assets/arizuko-client.js"`.
3. `GET /assets/missing.js` → 404 (no embedded match).
4. `OPTIONS /assets/arizuko-client.js` → 204 with CORS headers.
5. The sample page `/pub/examples/slink-sdk.html` loads the SDK,
   posts to a slink, and renders streamed frames.
6. `ant/skills/slink/SKILL.md` exists with the API table + HTML
   template + sample-page pointer.
7. SDK file is ≤ 250 LOC; spec is ≤ 200 lines.

## What changes

| File                                       | Change                              |
| ------------------------------------------ | ----------------------------------- |
| `webd/assets/arizuko-client.js`            | New SDK file (embedded)             |
| `webd/assets.go`                           | New: `embed.FS` + `handleAssets`    |
| `webd/server.go`                           | Register `GET /assets/{path...}`    |
| `webd/slink.go`                            | `/config` includes `"sdk": "..."`   |
| `webd/assets_test.go`                      | New: 4 tests (200, 404, CORS, MIME) |
| `webd/slink_test.go`                       | Assert `sdk` key in config          |
| `ant/skills/slink/SKILL.md`                | New skill                           |
| `template/web/pub/examples/slink-sdk.html` | New sample page                     |
| `specs/1/index.md`                         | Add Z2 row                          |

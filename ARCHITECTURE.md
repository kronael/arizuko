# Architecture

## Core vs integrations

arizuko is built as a **core** of always-present primitives plus
**integrations** that plug into the core per deployment. The split is
visible throughout this document and in the [README](README.md) adapter
table.
New integrations are added via the extension points described in
[EXTENDING.md](EXTENDING.md); the core evolves as a unit.

- **Core daemons / libraries** define the system shape: `routd` (router
  plane), `runed` (execution plane), `authd` (auth authority), plus
  store, ipc, auth, proxyd, webd, dashd, timed, onbod, vited,
  davd, the container runner, chanlib/chanreg. The package graph below
  is core.
- **Integrations** are pluggable: per-platform channel adapters
  (`teled`, `whapd`, `discd`, `slakd`, `bskyd`, `reditd`, `emaid`,
  `twitd`, `linkd`, `gitd`) talking to core over the channel protocol; optional
  capability hooks (Whisper transcription via `WHISPER_BASE_URL`, TTS
  via `ttsd` + `TTS_BASE_URL` (`specs/5/T-voice-synthesis.md`), oracle
  skill in `ant/skills/oracle/` driving the `codex` CLI,
  model gateway (`litellm` package + `MODEL_GATEWAY_URL`,
  `specs/6/23-llm-gateway.md`)).

The core runs with no channel adapter at all: a person reaches it through
the web chat or `arizuko talk`, an MCP client over proxyd. That is the
personal topology, one folder and no adapter, and it runs the same code as
a deployment with every adapter. A channel adapter is added per platform.

## Package Dependency Graph (core)

The platform splits into three core planes — `routd` (conversation /
routing), `runed` (execution), `authd` (auth authority) — each owning
its own tables. No single process wires everything; the
planes talk over HTTP (`/v1/*`) and verify each other's tokens offline.

```
routd/    conversation/routing plane (sole message appender, token verifier)
  ├── core          Config, types, Channel interface
  ├── store         the owner tables every daemon shares (library)
  ├── auth          identity, JWT verify, OAuth, middleware
  ├── chanreg       channel registry, HTTP channel egress (Deliverer)
  ├── router        message formatting, routing rules
  ├── ipc           per-turn agent MCP socket (ServeMCP, hosted in-process)
  └── diary, groupfolder
  routd tables: messages, routes, chats, secrets, acl, tasks, sessions, pane

runed/    execution plane (one KVM guest per turn: /dev/kvm,
        /dev/vhost-vsock, /dev/net/tun, and the per-turn egress proxy)
  ├── container     group setup, app-src staging, the egress rules
  │   └── groupfolder, mountsec
  ├── substrate     the declared ceiling and what a host delivers
  └── auth          verify only — the turn is credentialed by the guest's
                    vsock CID, not by a token
  runed tables: spawns, session_log, circuit_breaker
kernel/   the pinned guest kernel and guest-init, PID 1, built by
          `make kernel`

authd/    auth authority (sole ES256 signer, JWKS publisher, OAuth provider)
  └── auth          token mint/downscope, key rotation
  authd tables: signing_keys, refresh_tokens, auth_users, oauth_identities

timed/    scheduler: claims due scheduled_tasks, federates fires over routd
onbod/    onboarding state machine + admission queue
dashd/    operator dashboard (HTMX read views + routes/groups/secrets CRUD)
webd/     web chat channel adapter (HTTP/SSE, registers as "web")
vited/    Vite dev server / static origin behind proxyd
proxyd/   reverse proxy: auth, vhost routing, slink rate limiting
chanlib/  RouterClient, InboundMsg, auth middleware, URLCache (CDN-id
          proxy cache for discd/reditd), fsutil (CopyDirNoSymlinks,
          CopyFile), env helpers (EnvOr/EnvInt/EnvDur), ShortHash — shared
          by adapters + routd + container + onbod + webd
compose/  docker-compose generation
theme/    shared CSS + HTML helpers (used by onbod, dashd)
objstore/ the two storage primitives + the DynamoDB-protocol driver
          (dynstore); every daemon addresses this, never the engine
```

Every channel adapter, `webd`, and `proxyd` talk to `routd` over HTTP
(`/v1/*`). `routd` dispatches each turn to `runed` (`POST /v1/runs`);
`runed` spawns the agent guest. The per-turn agent MCP socket is hosted in-process by
`routd` (`ipc.ServeMCP`); `runed` bridges it to the guest over vsock, never
by a mount. The guest kernel: [`kernel/README.md`](kernel/README.md). What a
substrate declares and delivers: [`substrate/README.md`](substrate/README.md).

## Integrations

```
teled/ discd/ slakd/ bskyd/ reditd/ emaid/ whapd/ twitd/ linkd/ gitd/
        channel adapters — separate processes, register with core via
        the HTTP channel protocol (see "Channel Protocol" below)

sidecar/ whisper-cpp container; routd calls Whisper for inbound
        voice when VOICE_TRANSCRIPTION_ENABLED=true
egress/   forward proxy, one static allowlist, one client. runed starts
        one per turn on its tap's host side and stops it with the
        turn (see "Compose Containers" below)
anteval/  agent-capability gate: a black-box prober that runs real tasks
        through the public surfaces (REST/HTTP/MCP + a callback sink) against
        a LIVE instance and grades observable effects, not prose. Zero
        arizuko-internal imports; shippable separately. specs/5/9.
```

TTS (`ttsd/`, `specs/5/T-voice-synthesis.md`) and the oracle skill
(`ant/skills/oracle/`) ship as
optional integrations rather than core daemons; both opt-in via
env vars / folder secrets.

Some integrations have no daemon and no MCP surface at all —
**host-tool capabilities** are CLIs installed in the agent image
(or mounted from the host) that the agent runs as ordinary
subprocesses, with a SKILL.md as the discovery surface. `oracle`
is the canonical example. See `EXTENDING.md` "Host-tool
capabilities" for the pattern + the current list.

## Message Flow

Ingest, routing, prompt assembly and channel egress live in `routd`. The
guest lives in `runed`. One synchronous HTTP call joins them: `POST /v1/runs`.

<!-- twin:turn -->

```
┌─────────┐ POST /v1/messages ┌──────────────────────────────────────────┐
│ adapter ├──────────────────►│ routd: PutMessage, the sole appender     │
└─────────┘                   │ Enqueue; pollOnce re-enqueues a routed   │
     ▲                        │ chat with rows past its agent_cursor     │
     │                        └────────────────────┬─────────────────────┘
     │                                             ▼
     │                        ┌──────────────────────────────────────────┐
     │                        │ GroupQueue: one worker per folder        │
     │                        │ resolve: direct ► routes ► engagement    │
     │                        │ steer: sticky, /command, @child          │
     │                        └────────────────────┬─────────────────────┘
     │                                             ▼
     │ Typing on              ┌──────────────────────────────────────────┐
     │◄───────────────────────┤ runTurn: budget gate, topic fork, prompt │
     │ beginProgress          │ turn_context, session id, MCP socket     │
     │                        │ bound, acl ► typed booleans              │
     │                        └────────────────────┬─────────────────────┘
     │                                             │ POST /v1/runs
     │                                             │ RunOutcome returns
     │                                             ▼
     │                        ┌──────────────────────────────────────────┐
     │                        │ runed Manager.Run: a live guest takes a  │
     │                        │ steer frame; a full cap answers busy;    │
     │                        │ else it claims the spawns row            │
     │                        └────────────────────┬─────────────────────┘
     │                                             ▼
     │                        ┌──────────────────────────────────────────┐
     │                        │ qemuRuntime.spawn: VM record, staging    │
     │                        │ tree, virtiofsd, tap + rules, egress,    │
     │                        │ vsock ports, MCP bridge, QEMU            │
     │                        └────────────────────┬─────────────────────┘
     │                                             │ ctl frame: env, stdin
     │                                             ▼
     │                        ┌──────────────────────────────────────────┐
     │                        │ guest-init, PID 1: mounts, socat from    │
     │                        │ /run/ipc/gated.sock to vsock, then ant   │
     │                        └────────────────────┬─────────────────────┘
     │                                             │ reply, send,
     │                                             │ submit_status,
     │                                             │ submit_turn
     │                                             ▼
     │ POST <url>/send        ┌──────────────────────────────────────────┐
     │ the reply rows and     │ bridge ► routd socket: mcpDeliver, or    │
     │ one edited status row  │ recordTurnResult ► appendAndDeliver ►    │
     └────────────────────────┤ chanDeliverer.Send                       │
                              └──────────────────────────────────────────┘
```

<!-- /twin -->

- There is one `messages` table and one decision point. Nothing enqueues a
  turn around the router: a delegation, an escalation and a `#topic` re-post
  are each a row with `forwarded_from` or a topic, written by `PutMessage`
  and resolved like any inbound. Shortcuts beside the router drifted from it.
- Adapter ingest and the `pollOnce` backstop converge on
  `processGroupMessages` (`routd/dispatch.go`), so a slash command cannot
  race past the steer step.
- The MCP socket is bound before the run starts, because an agent whose
  first tool call finds no socket answers nothing.
- `router.FormatMessages` renders the batch as XML and tags a row an earlier
  failed turn left with `errored="true"`. The `acl` rows become four typed
  booleans on the run request, `ShareReadOnly`, `Egress`, `WebPublish` and
  `GHWrite`: runed consumes a decision and never reads a grant.
- The run outcome comes back on the `/v1/runs` response. The agent's words
  come back over the MCP socket, never on stdout.

Each step, its contract and its failure handling: `routd/README.md` § The
turn in routd, `runed/README.md` § The turn envelope, `ROUTING.md` § Resolution
Order. The same figure, for a newcomer: `concepts/turn.html`.

## Web Channel (proxyd)

Web chat uses `web:<folder>` JIDs. `webd` is a channel adapter that registers
with `routd` as `"web"` (prefix `web:`), receives messages via HTTP/SSE,
and stores them through the standard `/v1/messages` API. When
`routd.processGroupMessages` encounters a `web:` JID it splits the batch
by topic and dispatches one run per topic (first-seen order).

**Path model** (overview; full prefix table + DB-backed `web_routes`
fallthrough in `ROUTING.md` "HTTP Routing (proxyd)"):

- `/pub/*`, `/health`, `/invite/*`, `/p/*` — public
- `/assets/*` — public; webd serves the browser SDK it advertises in
  `/chat/<token>/config`
- `/slink/*` — 308-redirects to `/chat/<token>/`; header is `X-Chat-Token`
- `/chat/<token>/` (GET widget + POST) and `/hook/<token>` (POST only)
  — one `route_tokens` row each
  ([specs/5/W-webhook-routes.md](specs/5/W-webhook-routes.md)).
- `/pair/<token>` — identity pairing (GET confirm, POST redeem);
  authenticated, so an anonymous visitor bounces through OAuth and
  returns ([specs/5/31-identity-pairing.md](specs/5/31-identity-pairing.md))
- `/dash/*` — authenticated, proxied to dashd
- `/dav/*` — authenticated, proxied to dufs via a declared route (bespoke
  group-scoping + `davAllow` write-block; strips `/dav` prefix)
- `/*` — DB-backed `web_routes` longest-prefix, else authenticated to vited

**Auth** in `requireAuth`: `Authorization: Bearer <jwt>` → `refresh_token`
cookie → redirect to `/auth/login`. `authd` is the OAuth provider and
mints the ES256 JWT; its claims include `groups` — a JSON array of
allow-scopes computed by `auth.UserScopes(sub)` from the `acl` table
(transitively expanded via `acl_membership`). Operator membership
surfaces as `**` in this list; see "Operator" below. That list is a
projection of the rows `auth.Authorize` evaluates, never a verdict of its
own: it cannot carry an action or a `deny`, so it scopes a view and the
gate stays `Authorize`.
`webd.requireFolder` checks `X-User-Groups` on folder-specific endpoints.

WebDAV is gated by `WEBDAV_ENABLED`; compose points the `/dav/` route at
`http://davd:8080` and the dufs container mounts `groups/` read-only.
proxyd derives each world's host as `<world>.<HOSTING_DOMAIN>` and 302s it
to `/pub/<world>/` (no host-map file; `WEB_VHOST_ALIASES` overrides labels
that differ from the world name). Full protocol: `specs/5/V-web-vhosts.md`.

### Route generation pipeline

Routes are declared in two source-of-truth places and merged into the
serialized table proxyd loads at startup:

```
services/<adapter>.yaml (proxyd_routes block) ─────┐
                                                   ├─► compose.collectProxydRoutes
compose.coreProxydRoutes (dashd/webd/davd/onbod) ──┘             │
                                                                  ▼
                                   arizuko generate → proxyd_routes rows
                                                                  │
                                                                  ▼
                                    proxyd reads the table → []Route
                                                                  │
                                                                  ▼
                                  dispatchRoute longest-prefix match
                                                                  │
                                                                  ▼
                                    DB-backed `web_routes` fallthrough
                                                                  │
                                                                  ▼
                                          default: auth-gate to vited
```

Hand-wired in `proxyd/main.go` outside this table: `/auth/*`, `/health`,
`/pub/*` (with optional `PUB_REDIRECT_URL`), `/slink/*` (forwarded as an
ordinary route — the 308→`/chat/<token>/` redirect is issued by webd,
`handleSlinkRedirect`), `/chat/*` and `/hook/*` (route-token bearer),
`/dav/*` (`davAllow` + group-scope). All other routes flow
through the manifest declaration → `proxyd_routes` rows → dispatcher path.
Adding an adapter means dropping a `<name>.yaml` manifest (via `arizuko
packages add`); no proxyd or compose.go edits.

## Auth Hardening

authd signs every platform token and every daemon verifies offline. Cookie
flags, the Telegram widget window, provider allow-lists and the one
`issueSession` path: [`authd/README.md` § Keys, mints and
logins](authd/README.md#keys-mints-and-logins).

## Discoverability

Every HTTP-serving daemon mounts `GET /openapi.json` returning an
OpenAPI 3.1 doc generated from `resreg.Resource.RowType` reflection.
One walk over the registry produces `components.schemas` (struct field
→ property name via `json:` tag, Go kind → JSON Schema type) and
`paths./v1/<name>` (list / read-one / create / update / delete). Public —
no auth gate. Cached for the process lifetime; reflection is one-time at
first hit. Drift between handler + doc is structurally impossible because
both read the same struct. Spec: `specs/5/8-yaml-manifests.md`
§"OpenAPI emission". Aggregator: `/pub/arizuko/reference/openapi.html`.

## Cold tier vs hot tier

Two management surfaces, one handler, an injected gate per surface:

- **Cold tier** — operator config resources (`routes`, `acl`, `groups`,
  `secrets`, `scheduled_tasks`, `network_rules`, `web_routes`,
  `route_tokens`, `onboarding_gates`, `proxyd_routes`). One in-process
  `resreg.Resource` per resource wears **two faces**: an annotated REST
  `/v1/<name>` for humans + external tools, and a generated MCP facade
  (flat tool specs via `deriveMCPTools`) for the agent. Both dispatch the
  same handler in-process (`Store != nil`), so the audit row lands in the
  mutation's own batch. An HTTP forwarder (`Store == nil`, skips the local audit)
  is used only for a cross-daemon resource (`proxyd_routes` — proxyd owns
  the table, webd serves the MCP face). Spec
  [`5/17`](specs/5/17-openapi-mcp.md); rollout
  [`5/16`](specs/5/16-mcp-rest-unification.md). All seven agent-MCP faces
  (`web_routes`, `acl`, `routes`, `scheduled_tasks`, `network_rules`,
  `route_tokens`, `groups`) ride one `resreg.Resource`; the REST second
  face is folded onto the same handler for `web_routes`, `acl`, `routes`,
  `scheduled_tasks` and `secrets` (routd), plus onbod's `/v1/invites` and
  `/v1/gates` (`network_rules` is agent-only, no REST twin). Not yet folded:
  the hand-rolled `route_tokens` REST face, the `groups` REST twin, and
  one-owner federation.
- **Hot tier** — agent runtime actions (`reply`, `send`, `like`, `delete`,
  `engage`, `inspect_*`). **MCP-only by design** in `ipc/ipc.go`; no REST
  twin — an operator doesn't `reply` to a chat.
  `submit_turn` and `submit_status` sit below the hot tier: JSON-RPC methods
  on the same socket, absent from `tools/list`, with no REST twin and no
  resreg resource. They are delivery plumbing, not a managed entity.

`resreg` carries no auth policy — the authz gate is INJECTED per surface.
Operator REST uses the default `auth.Authorize` (scope/ACL); the
agent MCP socket injects
`db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` over the
folder's granted `acl` rows, unioned with the attested sender's own rows inside
that folder's subtree (`specs/5/33` §"Two axes"). Same folder-containment
discipline, two
identity sources (agent socket scope vs human JWT folder), two gates. Which
tools a caller even sees is `auth.EffectiveActions` over those same rows at
the agent socket, not resreg.

For the resources (`routes`, `scheduled_tasks`) whose _target_ folder is
what a mutation acts on, per-target containment is a routd-internal
per-face predicate (`containFn`) closed into the shared handler rather
than the `Gate`: the agent face re-runs `auth.Authorize` with the ACTUAL
target as the scope, so a delegated row scoped to a subtree covers exactly
that subtree; the REST face uses own-or-descendant folder containment
(`ownsFolder`). `web_routes` and `acl` need no such seam —
the caller acts on its own folder's row / scope, so their containment stays
in the injected `Gate`.

## The three planes

The router/executor/auth split is the only topology. Each plane is a
standalone daemon owning its own tables:

- **`routd`** — conversation/routing plane. Sole appender of messages,
  token **verifier** (not signer). Owns the `routd` tables (messages,
  routes, chats, sessions, acl, secrets, tasks, network_rules, pane_sessions),
  resolves routing rules, runs the orchestration loop, and dispatches
  each turn to `runed` (`POST /v1/runs`). Hosts the per-turn agent MCP
  socket in-process (`ipc.ServeMCP`) and builds the agent prompt. No
  docker socket, no egress proxies. Detail:
  [`routd/README.md`](routd/README.md).
- **`runed`** — execution plane. The only daemon wired to `/dev/kvm`,
  `/dev/vhost-vsock` and `/dev/net/tun`, one KVM guest and one egress
  proxy per turn. Owns the work
  queue, circuit breaker, `MaxConcurrent` cap, run TTL and steer
  (a ctl frame over vsock). It brokers no token: the turn is credentialed
  by the guest's vsock CID, a host-kernel fact a guest cannot forge, and
  reaches the per-turn MCP unix socket routd hosts through a uid-1000
  bridge `SO_PEERCRED` binds (`runed/README.md` § The peer credential). Owns
  the `runed` tables (`spawns`, `session_log`,
  `circuit_breaker` — `runed/runstore`). Detail:
  [`runed/README.md`](runed/README.md).
- **`authd`** — auth authority. The sole ES256 token minter; serves
  JWKS so every daemon verifies offline via the `auth/` library. Acts
  as the OAuth provider and mints `service:<daemon>` tokens from each
  daemon's `AUTHD_SERVICE_KEY`. Owns the `authd` tables (`signing_keys`,
  `auth_users`, `oauth_identities`, `refresh_tokens`). Spec
  [`specs/5/1-auth-standalone.md`](specs/5/1-auth-standalone.md).

`auth/` ships as a Go library (offline JWT verify, OAuth helpers, ACL,
middleware): it is the offline verifier that backends use against
`authd`'s JWKS. `routd` and other daemons call `authd` to
mint/downscope tokens rather than minting themselves.

## Daemon conventions

Durable rules the package layout obeys.

- **Naming.** Daemons end in `d` (`routd`, `runed`, `authd`, `proxyd`,
  `webd`, channel adapters). A daemon is a process, owns at most one set
  of tables, exposes REST and/or MCP. Short names use a ≤4-letter root +
  `d`: `routd` and `runed`, not `routerd`/`agent-runnerd`; the agent MCP
  host lives inside `routd`. Libraries don't end in
  `d` (`auth`, `audit`, `obs`, `resreg`, `chanlib`, `grants`, `core`,
  `types`): imported, never run. Utility packages follow Go convention
  (`httputil/`, not `http_utils/`). One Go module
  (`github.com/kronael/arizuko`); sibling shippable components stay
  inside it ([specs/6/16](specs/6/16-daemon-standalone-matrix.md)).

- **DAG library layering.** Libraries form a directed acyclic graph;
  downward imports only, no cycles, no library importing a daemon.
  Layer 0 (zero arizuko-internal deps): `types/`, `obs/`, `core/`.
  Layer 1 (depend on 0): `audit/`, `auth/`. Layer 2 (depend on 0+1):
  `resreg/`, `chanlib/`. Layer 3: daemons, importing layers
  0–2 plus other daemons' `api/v1/` packages. Cross-library imports
  inside one layer are fine.

- **`types/` shared-IDs package.** Top-level `types/` holds the
  cross-boundary IDs (`UserSub`, `Folder`, `Scope`) as pure
  named types — no behavior, no methods, zero arizuko-internal imports.
  It exists to break import cycles between each `<daemon>/api/v1/` and
  `core/`. Richer semantics (JID parsing, hierarchy, scope evaluation)
  stay in `core/` or the owning daemon (`types/identity.go`).

- **Per-daemon `<daemon>/api/v1/` contract.** Each daemon publishes a
  tiny `api/v1/` sub-package: wire types + a thin HTTP client (no
  business logic), zero arizuko-internal imports beyond `types/`. Other
  daemons import it freely; the daemon's implementation
  (`handler.go`, `db.go`) stays off-limits. REST and MCP are both
  implemented in the daemon over the same `api/v1/` types (MCP
  canonical, REST the impedance match). `v2/` lives next to `v1/` when
  the shape breaks. Adopt per daemon as its API stabilizes.

- **Table ownership.** Each daemon owns its own tables + REST/MCP APIs,
  and declares them beside the code that writes them: `routd` through
  `store/entities.go`, `runed` through `runed/runstore`, `authd` through
  `authd/authstore`, `onbod` through `store/entities.go`. Each owner creates its
  own at boot through `store.PrepareOwner`, and creates nobody else's. Libraries own no
  table and no API — a library that records something hands the caller a
  `Write` for the caller's own batch (`audit.WriteFor` is the model).
  Cross-daemon data access goes through the receiving daemon's `api/v1/`
  (REST for sync, MCP for agents). The `store/` package is the shared
  schema library, and ownership is enforced by what a daemon can reach:
  its own access key under a server policy bounded to the owners it
  needs.

  **One named exception: the hot path.** A daemon may read another owner's
  tables directly when the read is per-request on a serving
  path. `proxyd` is the only one that qualifies — route-token lookup and the
  `acl` read behind `X-User-Groups`, on every inbound request — because a
  login-time scope snapshot would leave a revoked grant working for the
  token's lifetime, and freshness there is a security property. Any new
  direct cross-owner read must name the per-request path it sits on or it is
  a violation. Decided and bounded in
  [`specs/5/16`](specs/5/16-mcp-rest-unification.md) §"The hot-path
  exception", pinned by `proxyd/hotpath_read_test.go`.

- **No dual-version period.** Cutovers are one-shot: a migration
  ships in one release, old paths delete in that same release. No dual
  API periods, no `v1`/`v2` side-by-side as a migration tool. Recovery
  is `git revert`, not a compat layer. Build order may be staged;
  the cutover is one commit.

## Channel Protocol

Channels are external processes registering via HTTP. Both sides are HTTP
servers. Full protocol: `specs/5/34-channel-protocol.md`.

- **Inbound**: `POST /v1/messages` → store → route
- **Outbound**: router calls `POST <url>/send` (synchronous)
- **Registration**: `POST /v1/channels/register` with name, URL, JID prefixes,
  capabilities; returns session token
- **Health**: `GET /health` every 30s; 3 failures = auto-deregister; a send to a
  deregistered adapter fails and routd's 30s sweep re-sends the `pending` row
- **Auth**: an ES256 `service:<daemon>` token exchanged from
  `AUTHD_SERVICE_KEY` at authd, verified against authd's JWKS
  (`chanlib.Auth`). Both directions use it; there is no shared symmetric
  secret.
- **Typing**: `/typing` handlers route through `TypingRefresher`
  (`chanlib.TypingRefresher` / `whapd/src/typing.ts`) — re-sends presence on
  short interval with hard TTL. routd owns the terminator (`routd/progress.go`):
  it clears typing when the turn ends, not at the first reply, for every chat
  the finished run owned (`turn_context.run_id`), which covers a follow-up
  steered into that run from a different chat of the same folder. A run that
  returns without ending its turn clears it too. slakd and gitd, which have no
  native indicator, render typing as a `👀` reaction on the trigger message
  (`chanlib.AckMark`); a graceful shutdown leaves it standing, because the
  turn may still be running, and the next boot removes a recorded one older than
  the TTL. The
  adapter TTL is the backstop and is 90% of `RUNED_RUN_TIMEOUT` — strictly
  below the wall, or it only fires once the guest is already dead (BUGS
  `T1`).

Packages: `chanreg/` (registry, health, `HTTPChannel`, the egress
Deliverer), `routd/` (router-side `/v1/*` handlers), `chanlib/`
(shared by Go adapters).

## Inbound Media Pipeline

1. Adapter populates `Attachments []InboundAttachment` in `/v1/messages`
2. routd `PutMessage` stores attachment JSON
3. Enricher fetches URLs, writes to `groups/<folder>/media/<YYYYMMDD>/`,
   calls routd `EnrichMessage` to prepend
   `<attachment path="..." mime="..." filename="..."/>` and clear column
4. Voice (`audio/*`) → Whisper if `VOICE_TRANSCRIPTION_ENABLED=true`
5. Guest path: `/home/node/media/...`

teled serves `GET /files/{fileID}` as Telegram CDN proxy (bot-token URLs are
ephemeral). discd uses direct CDN URLs.

Config: `MEDIA_ENABLED=true`, `VOICE_TRANSCRIPTION_ENABLED=true`,
`WHISPER_BASE_URL`, `WHISPER_MODEL=turbo`.

## Key Types (core)

| Type          | Purpose                                           |
| ------------- | ------------------------------------------------- |
| `Config`      | settings from `.env` + env vars                   |
| `Message`     | incoming (sender, content, reply context)         |
| `Group`       | registered (folder, name, config, parent)         |
| `GroupConfig` | per-group: mounts, timeout                        |
| `Route`       | routing table entry (seq, match, target)          |
| `Task`        | scheduled (cron, prompt, status)                  |
| `Channel`     | Connect, Send, SendFile, Owns, Typing, Disconnect |

## Data ownership

Every table is an objstore entity: one item per row at a `(partition, sort)`
coordinate, with string attributes (`objstore/README.md`). One
DynamoDB-protocol server holds every owner's tables as `<owner>.<entity>`,
named by `DYNAMODB_URL`. The store, from a daemon down to PostgreSQL, is the
figure at the top of `objstore/README.md`.

An owner is a daemon and the tables it alone creates. Each daemon signs with
its own key, and `store/owner_reach.go` `StoreReach` lists the owners each
daemon opens; `TestStoreReachMatchesTheCallSites` fails when the code and the
list disagree.

```
┌─────────┐ written by routd
│ routd.* │◄── also opened by onbod, dashd, webd, proxyd, gitd, slakd,
└─────────┘    cmd/arizuko
┌─────────┐ written by onbod
│ onbod.* │◄── also opened by dashd, cmd/arizuko
└─────────┘
┌─────────┐ written by runed
│ runed.* │◄── no other daemon; dashd reads runed's /v1
└─────────┘
┌─────────┐ written by authd
│ authd.* │◄── also opened by cmd/arizuko, for the signing keys
└─────────┘
┌──────────────┐ written by that adapter; gitd and slakd also open
│ <adapter>.*  │ routd.*
└──────────────┘
timed and ttsd hold no store key: they call an owner's /v1
```

| Owner        | Entities                                                                                                   | Declared in                            | Column detail                                |
| ------------ | ---------------------------------------------------------------------------------------------------------- | -------------------------------------- | -------------------------------------------- |
| `routd`      | messages and their four indexes, routing, grants, secrets, sessions, turns, tasks, packages                | `store/entities.go`                    | `store/README.md` § Every table is an entity |
| `onbod`      | `onboarding`, `onboarding_gates`, `invites`, `invite_redemptions`                                          | `store/entities.go`                    | `store/README.md` § Every table is an entity |
| `runed`      | `spawns`, `session_log`, `circuit_breaker`                                                                 | `runed/runstore`                       | `runed/README.md` § Entities owned           |
| `authd`      | `signing_keys`, `signing_key_slot`, `auth_users`, `oauth_identities`, `refresh_tokens`, `refresh_families` | `authd/authstore`                      | `authd/README.md` § Entities owned           |
| each adapter | `outbound_dedup`, `inbound_pending`, `file_urls`, `ack_marks`; emaid adds `email_threads`, `email_msg_ids` | `chanlib/chanstore`, `emaid/emaistore` | `chanlib/README.md` § Storage                |

- **`audit_log` is in all four owners.** One declaration lives in `audit`,
  each opener creates it (`audit.EnsureEntities`), and correlation across the
  four is `turn_id`. proxyd, webd and dashd own no audit table and record
  through routd's.
- **Why a second daemon opens routd's tables.** onbod and dashd hold the
  operator's onboarding and admin writes; webd resolves a route token and
  reads a chat; gitd reads the sealed webhook secret per delivery; slakd
  reads the pane context. proxyd reads `acl` on every request, because a
  login-time snapshot would keep a revoked grant working for the token's 15
  minutes (`proxyd/hotpath_read_test.go`). Everything else goes through the
  owner's `/v1`.
- **No foreign key, so nothing cascades.** A group delete purges every scope
  that confers on the folder or routes into it, by one containment predicate,
  and a table in neither of `store/folder_scope.go`'s lists fails its
  completeness test.

`messages.source` is the adapter of record, stamped by routd's
`handleMessages`; outbound reads `LatestSource(jid)`. All agent output,
delegation and escalation flow through `PutMessage`. A bot row carries
`is_from_me` and `is_bot_message` and is filtered from inbound polling.

### Why the store layer is shaped this way

1. **The engine is a server, not a library.** ExtendDB over PostgreSQL speaks
   the DynamoDB wire protocol, and arizuko is one client of it through a
   driver on the official SDK. No component holds a state of record on local
   disk. The protocol was chosen because it has transactions, conditional
   writes and a second implementation to fall back to, and because the same
   driver reaches Amazon's service by changing one `BaseEndpoint`.
2. **There is no database layer.** `objstore.Store` is two methods, `Query`
   and `GuardedBatch` (`objstore/objstore.go`), derived from the five
   operations arizuko needs: containment by path prefix, the guarded batch,
   the versioned replace, the reachability guard and the claim-and-return. A
   caller states a coordinate and a condition, never a plan, so the engine
   can change without a rewrite. `tests/components/imports_test.go`
   `TestNoPackageOpensSQL` fails the build if any package reaches
   `database/sql`.
3. **A deployment is an account, not a database.** The `tables` registry is
   keyed `(account_id, table_name)`, so one server can hold every instance on
   a host and keep them apart on the account. A PostgreSQL role is NOT the
   tenancy boundary and must not be used as one.
4. **The engine splits its own state in two.** `<data-db>` holds one
   PostgreSQL table per DynamoDB table; `<data-db>_catalog` holds accounts,
   IAM, the table registry and `settings`. They differ in size, in read
   pattern and in what a restore destroys, so a control-plane read never
   contends with a tenant's scan and a tenant restore cannot take the account
   registry with it. The `connection_string` names the **catalog**, which is
   the root of discovery: `settings` carries the data database's name and
   connection string, along with the deployment's encryption key.

Deploy procedure: `INSTALL.md` § The store server.

## Guest Lifecycle

`runed` is the only daemon that starts a turn: one KVM guest per turn, bound
to one group folder and one tap, as the Message Flow figure shows. The guest
runs one turn and powers off. The folder is the agent's home, so the session
jsonl, the diary and the skills persist between turns, and the context grows
per group because each turn resumes the session. Sibling groups get their own
guests on their own taps, so the boundary between tenants is the group, not
the turn. Each group keeps one context: unrelated concerns in one context make
the model blend them, so sales and on-call never share one.

What runed renders into a turn, how it boots the guest and how it ends it:
[`runed/README.md` § The turn envelope](runed/README.md#the-turn-envelope).
The session id a turn resumes: [`ROUTING.md` § Topic Routing](ROUTING.md#topic-routing).
Threat model: `SECURITY.md` § Model.

### Secret injection

A model key enters the guest's env at spawn. A capability credential stays
on the host and routd spends it inside one tool call. With the `litellm`
package the model key stays at the gateway too, and the gateway is the
spend ledger. Figure and rules: [`SECURITY.md` § Secret
injection](SECURITY.md#secret-injection).

## IPC (ipc package)

MCP server on a unix socket (`mark3labs/mcp-go`), hosted in-process by
routd (`ipc.ServeMCP`, called from `routd.ServeTurnMCP`). One per turn
at `ipc/<folder>/gated.sock`. Tool visibility is `auth.EffectiveActions`
over the caller's `acl` rows — does it hold the action at ANY scope; each
call is then authorized by the agent gate
`db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` routd
injects, not the
plain operator `auth.Authorize`. One row set feeds both, so the tool list
and the per-call gate agree by construction. `list_acl` inspects the
effective ACL rows for the caller's principal set.

Inside the guest, `guest-init` starts `socat` bridging a guest-local unix
socket to vsock; the agent configures the `arizuko` MCP server in
`settings.json` with `socat UNIX-CONNECT`. A tool call crosses the vsock to a
uid-1000 bridge runed starts per turn, which dials routd's socket. routd
binds the socket before runed spawns the guest and removes it when the run
returns; runed never mounts it (`runed/README.md` § The peer credential).

Identity resolved from the socket path (folder); authorization runs
the injected agent gate (`db.Authorize` with `mcp:`+tool name), never a
second hand-rolled check.

## Queue (queue package)

Two layers serialize work. routd's `GroupQueue` serializes inbound
dispatch per folder; runed's Manager enforces the guest
concurrency cap, circuit breaker, and steer at the spawn boundary.

routd-side `GroupQueue`:

- per-group active flag + container name; `EnqueueMessageCheck(jid)` is
  signal-only — the queue calls back into routd's `processGroupMessages`
  (`routd/loop.go:235`, set with `SetProcessMessagesFn`)
- a follow-up for a folder with a live turn is steered into it: routd
  dispatches to runed again, runed sends a `steer` ctl frame over vsock and
  waits for the guest's `steer_ack` (`runed/vmctl.go`), `guest-init` writes
  the batch as files under `/run/ipc/input`, and ant drains them between
  tool calls (`ant/src/index.ts` `pollIpcDuringQuery`)

runed Manager (`runed/manager.go`):

- `MaxConcurrent` global cap (`MAX_CONCURRENT_CONTAINERS`, default 5),
  one live spawn per folder
- circuit breaker: 3 consecutive folder failures opens; reset by a new run
- run TTL (`RUNED_RUN_TIMEOUT`, default 20m); hourly sweep of spawns past a
  seven-day retention, which is what keeps their one partition a window
- steer into a live spawn via a `{steer}` ctl frame over vsock, acked by
  the guest before the closure returns true (`runed/README.md` § Steer and
  stop)

Delegation, escalation, and `#topic` routing are not special — each writes
a row via `PutMessage` and calls `EnqueueMessageCheck`.

## Routing

Route table, route types, topic routing, reply routing, sticky routing,
full flow examples: `ROUTING.md`. The autoviv pattern (an agent holding
`mcp:register_group` calls it to auto-create per-channel children when
unrouted JIDs arrive) is
`template/web/pub/arizuko/concepts/autoviv.html` — no new mechanism, just
routes + MCP + a delegated grant composing.

## Authorization (auth package)

One row, one evaluator: `acl(principal, action, scope, params, predicate,
effect, grant_option)` → `auth.Authorize`. Deny wins; no matching allow row
is a deny. `auth.EffectiveActions` is the `tools/list` view over the same
rows, and `auth.Delegate` bounds re-delegation to a subset of rows held WITH
GRANT OPTION. Guest capabilities resolve from `acl` at dispatch into typed
booleans on the run request. The figure, the agent socket's two axes and
every rule: [`auth/README.md` § Authorization is one row and one
question](auth/README.md#authorization-is-one-row-and-one-question).

## Operator

An operator is a member of `role:operator`, which holds the seeded row
`(*, **)` WITH GRANT OPTION. There is no operator flag and no sentinel.
`arizuko group <inst> grant <sub> '**'` adds the membership, `ungrant`
removes it, and `auth.Authorize` treats the operator like any other
principal.

## Compose Containers

`compose.Generate(dataDir)` builds `docker-compose.yml` from:

1. **Built-in** — every core stanza is ALWAYS emitted; `profiles:` decides
   what docker starts. `authd`+`routd`+`runed` carry no profile (the three
   planes, always up); `webd`+`proxyd`+`vited`+`dashd` are `web`, then
   `timed`, `onbod`, `davd`.
2. **Extra** — one `include: ./services/<name>.yml` per package fragment in
   `<dataDir>/services/`; docker loads them, compose.go never parses them.

`COMPOSE_PROFILES` in the data dir's `.env` is the single gate. Generate
derives it from the feature flags (`WEB_PORT`, `WEBDAV_ENABLED`,
`ONBOARDING_ENABLED`) and rewrites it, with
`APP`/`FLAVOR`/`DATA_DIR`, inside a `# --- compose-managed` block that leaves
operator lines untouched. Fragments interpolate those three for their
container name and host mount — identity is configured, never derived.

Adapters reach the router at `ROUTER_URL=http://routd:8080`. `runed` is the
only built-in service granted `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`
and `NET_ADMIN`/`SYS_ADMIN` (spec `6/41` §"The runed deploy topology");
`authd` exposes JWKS for offline verification by the rest.

Bundled catalog at `template/services/`: a `<name>.yml` compose fragment and
a `<name>.yaml` manifest for each of `bskyd`, `discd`, `emaid`, `extenddb`,
`gitd`, `linkd`, `litellm`, `reditd`, `slakd`, `teled`, `ttsd`, `twitd` and
`whapd`, plus `kokoro.yml` alone.

Fragment format — a plain partial compose file:

```yaml
services:
  teled:
    container_name: ${APP}_teled_${FLAVOR}
    image: arizuko:latest
    entrypoint: ['teled']
    env_file: ['../env/teled.env'] # relative to services/
    environment:
      ROUTER_URL: 'http://routd:8080'
    depends_on: [routd]
    restart: on-failure
```

Container naming: `<app>_<service>_<flavor>` (e.g. `arizuko_teled_krons`).
`arizuko packages <instance> add <name>` links a fragment (plus its
`<name>.yaml` manifest, if any) into `/srv/data/arizuko_<flavor>/services/`.
A package ships exactly two file kinds: `.yml`, a partial compose file docker
`include`s, and `.yaml`, the `5/8` manifest whose rows `arizuko apply`
installs.

All Go daemons listen on :8080 internally except ttsd at :8880. The
third-party images keep their own: kokoro :8880, litellm :4000.

Third-party fragments (`kokoro.yml`, `litellm.yml`) never read the shared
`.env`. kokoro gets a generated `env/kokoro.env` holding its one key;
litellm and its `litellm_db` have no env file at all — docker interpolates
the keys the fragment names (`${MODEL_GATEWAY_KEY:?}`,
`${MODEL_GATEWAY_DB_PASSWORD:?}` and the credential pair) from `.env`, and
`compose/fragments_test.go` pins that set, so handing either image a new
instance value is a visible edit. Both secrets are minted into `.env` by
`packages add` (the `env` block of `litellm.yaml`), so the `:?` is a guard
against a hand-copied fragment, not a step the operator performs.

Egress isolation is not a setting; it is a property of the turn substrate.
There is no proxy service in the compose file: `runed` starts ONE `egress`
process per turn on the host side of that turn's own tap, with the folder's
allowlist baked in, and stops it with the turn (spec `6/41` §"Egress by name,
sealed below the kernel"). The tap's rule set accepts only that guest's
traffic to the proxy and drops everything else in both directions; the guest
has no other route and no resolver, so a host name is meaningful only inside
a request to the proxy. runed refuses to start when the `egress` binary is
missing. See `egress/README.md` and `SECURITY.md` (Network egress isolation).

## Onboarding (onbod/)

Self-service token-based onboarding with optional admission gates. Turns
inbound JIDs into provisioned groups via OAuth + `container.SetupGroup`.
See `onbod/README.md`.

## Scheduler (timed/)

Standalone daemon that fires due `scheduled_tasks` as messages. It
federates the fire loop over routd (`ROUTER_URL`), authenticating with a
`service:timed` token exchanged from `AUTHD_SERVICE_KEY` at `AUTHD_URL`;
routd picks the message up via normal poll. See `timed/README.md`.

## Operator Dashboard (dashd/)

HTMX portal over routd's data plus a write surface (routes
editor, groups CRUD, per-user secrets) requiring authenticated access.
Spec: `specs/6/28-cockpit-index.md`. See `dashd/README.md`.

Operator-only pages reached from `/dash/services/` are the per-daemon control
planes: `/dash/routd/`, `/dash/runed/`, `/dash/proxyd/` and `/dash/authd/`
(signing-key lifecycle, login sessions, per-login sign-out — spec `5/1`). Each
goes over HTTP to the owning daemon's `/v1` with the `service:dashd` bearer
rather than reading its tables, which is what keeps the owner the single writer
and puts each mutation's audit row in the mutation's own batch. dashd reaches
routd's and onbod's tables directly; runed's and authd's only through their
daemons.

## Diary (diary package)

`diary.Read(groupDir, max)` reads recent `.md` from `group/diary/`, extracts
`summary:` frontmatter, returns XML with age labels (today, yesterday, N
days/weeks ago). Prepended to agent prompt as `<knowledge layer="diary">`.

Two-layer memory: `MEMORY.md` for long-term, daily files for work log. Nudged
by `/diary` skill, PreCompact hook, every 100 turns. Spec:
`specs/1/L-memory-diary.md`.

Episodes (compressed transcripts) follow the same `summary:` format, indexed
by `/recall`. Compression: daily → weekly → monthly. Spec:
`specs/4/17-knowledge-system.md`.

## Observability

Three substrates, clearly split:

- **`audit_log` entity** — source of truth for state-changing
  operations, one per owner. Where the mutation is local the row rides its
  batch (`audit.WriteFor`), so a refused mutation records nothing and a
  recorded one happened; where the effect lands on a remote — a platform
  message, a guest — the row is written alongside it
  (`audit.EmitTo`), since there is no batch to join. Each owner
  serves its own table read-only at `GET /v1/audit` (one `resreg`
  registration, mounted by routd/runed/authd; `audit.Query` is the single
  reader), and `/dash/audit/` federates them over those APIs rather than
  reading another owner's tables. A successful read writes no row — `list` does not
  mutate — but denials and errors do. Spec
  [`5/I`](specs/5/I-tool-call-logging.md).
- **slog → journald** — operational telemetry for everything (state
  changes + reads). High-rate, lossy by design (journald rotation,
  level filtering). `journalctl -u arizuko_<inst>` is the default
  operator view.
- **OTLP export (opt-in)** — slog stream tee'd to any OTel-compatible
  collector when `OTEL_EXPORTER_OTLP_ENDPOINT` is set in the instance
  `.env`. Logs not spans; `turn_id` → deterministic TraceID
  (`sha256(instance + "/" + turn_id)[:16]`) across daemons, so a
  collector (Grafana / Tempo / Datadog / Honeycomb) groups one turn.
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` adds spans, `METRICS_ENABLED` a
  Prometheus `/metrics`; every one unset → zero overhead, stderr JSON only.
  Library: [`obs/`](obs/) (wiring rules in its README). Spec:
  [`5/O`](specs/5/O-observability.md). Off by default.

## Error Handling

Per-message `errored` flag (`messages.errored`). No
per-chat quarantine.

- Agent error, no output: routd `MarkMessagesErrored(ids)` tags the
  failing batch; cursor stays so the batch reappears next poll. The
  prompt carries `errored="true"` on those rows so the agent sees
  it failed the last attempt and must try differently.
- Agent error with output: same tag + cursor advances (partial work
  preserved).
- Circuit breaker: 3 consecutive failures →
  `routd.Loop.onCircuitBreakerOpen` calls routd `DeleteErroredMessages`
  and resets the session. No permanent quarantine — the next inbound
  message gets a clean run.
- Turn timeout: a `{stop}` frame (`SIGTERM` to the agent, then power-off) →
  `SIGKILL` on the QEMU pid if the deadline still passes.

## Long-running tasks — the turn deadline

A turn has one deadline. `RUNED_RUN_TIMEOUT` (default 20m) is the run
ceiling, lowered by the group's `container_config` timeout when one is set
(`runed/manager.go` `turnTTL`). runed hands the guest a query timeout 30
seconds under that ceiling, floored at 60 seconds, as
`ARIZUKO_QUERY_TIMEOUT_MS` (`runed/render.go` `queryTimeoutMs`), so the
harness aborts and reports before runed kills the guest under it. With the
default ceiling the log line reads:

```
Query timeout (1170000ms) reached, aborting
```

User-visible impact: the in-flight reply is dropped, and inbound messages
that arrived during the timed-out turn are tagged `errored=1` in `messages`.
The next inbound gets a fresh guest; the errored rows stay flagged and show
as failed turns in the dashboard.

Mitigations for skill authors and agents (no platform change required):

- **Split into multiple turns** — emit partial results, ask the user
  for "continue" rather than packing a 20-minute job into one turn.
- **Checkpoint progress** — write intermediate state to `~/facts/`
  or `~/tmp/` between subtasks so a fresh guest can resume
  instead of restarting from zero.
- **Fewer parallel subagents** — every Task subagent shares the
  parent turn's wall budget; 8 parallel critics is 8× the chance of
  one stalling out the whole turn.
- **Background long jobs** — pass `run_in_background: true` to the
  Task tool for genuinely long work; the parent turn returns
  immediately and the agent can poll/resume later.
- **Self-terminate agentic loops** — count actual iterations and
  stop well before the cap; a loop that ran 28 of an intended 30
  iterations and saved partial output is better than one that hit
  the deadline mid-iteration 30 and dropped everything.

## Prompt Assembly

Every inbound turn routd emits an envelope of small XML-shaped
blocks, prepended (or attached) to the agent's prompt. They share
three properties: **XML-shaped**, **never persisted to `messages`**,
**per-turn scope only** (recomputed next turn). The blocks emitted
today:

- `<autocalls>` — zero-arg facts (`now`, `instance`, `folder`,
  `session`); `routd/prompt.go`
- `<persona name=…>` — `PERSONA.md` frontmatter `summary:` re-anchor;
  `routd/prompt.go`
- `<previous_session/>` — last session id/timing on a fresh session;
  `routd/prompt.go`
- `<knowledge layer=…>` — recent diary entries with age labels;
  `diary/diary.go`
- `<messages>` + `<reply-to>` + `<message>` — inbound batch;
  `router/router.go`
- `<attachment …/>` — inbound media path + optional `transcript=`;
  `routd/enrich.go`
- `<observed>` — trailing window of `is_observed=1` messages stored
  under this folder via `#observe` routes; `routd/prompt.go`
- `<topic name=…/>` — scope envelope on every turn (empty for main).
  Parent topic context arrives via the forked Claude Code session,
  not via injection (`ROUTING.md` § Topic Routing); `routd/prompt.go`
- `<surface>slack-pane</surface>` + `<pane-context jid=…/>` —
  emitted when the trigger arrives via an open Slack assistant pane
  (spec 8/D); `routd/prompt.go`

The convention for adding a block lives in `routd/README.md`. See
`EXTENDING.md` for the autocall extension point in particular.

## MCP Surface

Action tools mutate state: messaging (`send`, `reply`, `send_file`,
`forward`, `edit`), feed (`post`, `quote`, `repost`, `like`,
`dislike`, `delete`), message actions (`pin_message`, `unpin_message`,
`unpin_all`, `edit`, `delete` — `delete`/`edit` are own-message by
default, `:any` is a distinct grant; spec 5/Z), control
(`schedule_task`, `register_group`, `set_routes`, …). Adapters lacking
a native primitive return a typed `chanlib.UnsupportedError` whose hint
redirects to a concrete alternative (e.g. `dislike` on emoji platforms
hints `like(emoji='👎')`). Read-only introspection lives in the
`inspect_*` family (`ipc/inspect.go`): `inspect_messages`,
`inspect_routing`, `inspect_tasks`, `inspect_session`,
`inspect_identity`. `find_messages` is ranked search over
message content (bm25 ranking + «»-marked snippet; all terms required,
`"phrase"`, `-exclude`; spec 5/C, `routd/msgsearch`). A `/root`-elevated turn sees every folder; an ordinary
turn is scoped to its own folder subtree. Full tool table in
`ant/skills/self/SKILL.md`.

## Data dir

`/srv/data/arizuko_<name>/` per instance (`${PREFIX:-/srv}/data/`):

- `.env` — config; daemons read it from cwd (`core.LoadConfig`). Anchor
  vars: `AUTHD_SERVICE_KEY`, `AUTH_SECRET`, `HOST_DATA_DIR`,
  `RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`, `WEB_HOST`, `ASSISTANT_NAME`;
  per-daemon vars in each
  `<daemon>/README.md`. Business state (gates, grants, onboarding) lives in
  the DB; infra toggles live in env.
- `store/` — the instance's state. `store/extenddb/` holds the server's config
  and certificate and `store/extenddb_db/` its PostgreSQL data directory, so a
  tar of the data dir still carries every row. Adapters keep private
  `store/<name>/` dirs for the files they own.
- `groups/<folder>/` — group files, logs, diary; `media/<YYYYMMDD>/` holds
  downloaded inbound attachments; `.claude/` holds agent session state.
- `ipc/<folder>/` — MCP unix sockets.
- `web/pub/`, `web/priv/` — the unified web trees the per-group slots
  project into (table below).

## Design principles

**Simple stays simple, complex goes deeper.** arizuko's primitives scale
with need: `solo/inbox` and `corp/eng/sre/oncall/launch-q3` run the same
code. Every primitive has a one-line setup AND a deep-config path — group
hierarchy (arbitrary depth), topic kinds (default thread or
`task`/`meeting`), grants (the seeded `role:member` floor or per-folder
rules), channels (env-var trivial, dashd-managed), secrets (folder-scoped
by default, user-scoped when needed). Don't force structure where it isn't
needed; don't fight it where it is.

## Guest mount layout

A guest sees its files through two virtio-fs shares: the release rootfs,
read-only, and one staging tree that mirrors guest paths. The figure, the mount
table, the read-only rule and `mountsec`: [`runed/README.md` § Mounts](runed/README.md#mounts).

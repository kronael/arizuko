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

A minimal deployment runs core plus one channel adapter; a maxed-out
deployment runs all of them.

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
  ├── container     mount rendering, app-src staging, the egress rules
  │   └── groupfolder, mountsec
  └── auth          verify only — the turn is credentialed by the MCP
                    socket (SO_PEERCRED), not by a token
  runed tables: spawns, session_log, circuit_breaker

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
by a mount (spec `6/41`).

## Integrations

```
teled/ discd/ slakd/ bskyd/ reditd/ emaid/ whapd/ twitd/ linkd/ gitd/
        channel adapters — separate processes, register with core via
        the HTTP channel protocol (see "Channel Protocol" below)

sidecar/ whisper-cpp container; routd calls Whisper for inbound
        voice when VOICE_TRANSCRIPTION_ENABLED=true
egress/   forward proxy, one static allowlist, one client. runed starts
        one per turn on its tap's host side and stops it with the
        turn (see "Compose Containers" below); specs/6/41
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

Ingest, routing, prompt assembly, and channel egress live in `routd`;
the guest spawn lives in `runed`. The hand-off is one HTTP call
(`routd → runed POST /v1/runs`); everything else is in-process to one
of the two planes.

```
Channel adapter → POST /v1/messages (routd) → PutMessage (sole appender)
  → routd.Loop.pollOnce (polls every PollInterval)
  → NewMessages (since lastTimestamp)
  → resolveGroup (route table lookup)
  → handleCommand (prefix dispatch)
  → route lookup: trigger fires a turn; #observe stores only
  → on route miss or #observe, fires turn if chat_reply_state.engaged_until active
  → enrichAttachments: download media → Whisper for voice
  → router.FormatMessages (XML batch, errored rows tagged errored="true")
    + acl lookup → typed ShareReadOnly/Egress/WebPublish on the run request
  → routd stands up the per-turn MCP socket in-process (ipc.ServeMCP)
  → routd → runed: POST /v1/runs (dispatch the turn)
    → runed: steer into a live guest (a ctl frame over vsock) OR boot a new one
    → runed exports the ipc dir over virtio-fs and bridges the guest's MCP port
      from its own CID (the CID IS the turn credential, spec 6/41)
  → agent runs the turn; per-turn results return over MCP (submit_turn)
    and the run outcome returns on the /v1/runs response
  → routd/progress.go: Typing(on) at dispatch; one ⏳ message created by the
    first status and edited after, only on a channel that advertises edit;
    finishTurn clears Typing and edits ⏳ to ✓ or ✗
  → routd: chanreg Deliverer → POST <adapter>/send (channel egress)
```

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

- Secure cookies when `authBaseURL(cfg)` is HTTPS
- Login rate limit: 5 POST `/auth/login` per IP per 15min (in-memory)
- Telegram widget: rejects `auth_date` > 5min (replay protection)
- OAuth: Google (optional `GOOGLE_ALLOWED_EMAILS`), GitHub (optional
  `GITHUB_ALLOWED_ORG`), Discord, Telegram widget; shared
  `issueSession` path in `authd/oauth.go`

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
  docker socket, no egress proxies. Spec
  [`specs/5/E-routd.md`](specs/5/E-routd.md).
- **`runed`** — execution plane. The only daemon wired to `/dev/kvm`,
  `/dev/vhost-vsock` and `/dev/net/tun`, one KVM guest and one egress
  proxy per turn. Owns the work
  queue, circuit breaker, `MaxConcurrent` cap, run TTL and steer
  (a ctl frame over vsock). It brokers no token: the turn is credentialed
  by the guest's vsock CID, a host-kernel fact a guest cannot forge, and
  reaches the per-turn MCP unix socket routd hosts through a uid-1000
  bridge `SO_PEERCRED` binds (spec `6/41` §"The peer credential"). Owns
  the `runed` tables (`spawns`, `session_log`,
  `circuit_breaker` — `runed/runstore`). Spec
  [`specs/5/P-runed.md`](specs/5/P-runed.md).
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

- **No backward compatibility.** Cutovers are one-shot: a migration
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

## Schema

Every table is an objstore entity: one item per row at a `(partition, sort)`
coordinate, three columns wide (`objstore/README.md`). A DynamoDB-protocol
server holds every owner's tables as `<owner>.<entity>` (`dynstore.Prefix`),
named by `DYNAMODB_URL`. The coordinate is what a read pays for, and
`store/entities.go` argues each one beside the code that writes it.

### Why the store layer is shaped this way

Four decisions, each with its spec:

1. **The engine is a server, not a library.** ExtendDB over PostgreSQL speaks
   the DynamoDB wire protocol, and arizuko is one client of it through a
   driver on the official SDK (`specs/6/42`). No component holds a state of
   record on local disk. The protocol was chosen because it is the one with
   transactions, conditional writes and a second implementation to fall back
   to, and because the same driver reaches Amazon's service by changing one
   `BaseEndpoint`.
2. **There is no database layer.** `objstore.Store` is TWO methods, `Query`
   and `GuardedBatch` (`objstore/objstore.go:47`), and `specs/6/40` derives
   them from the five operations arizuko actually needs: containment by path
   prefix, the guarded batch, the versioned replace, the reachability guard
   and the claim-and-return. A caller states a coordinate and a condition; it
   never states a plan. That is what lets the engine change without a
   rewrite. `tests/components/imports_test.go` `TestNoPackageOpensSQL`
   fails the build if any package reaches `database/sql`.
3. **A deployment is an account, not a database.** The `tables` registry is
   keyed `(account_id, table_name)`, so one server can hold every instance on
   a host and keep them apart on the account (`specs/6/43`). A PostgreSQL
   role is NOT the tenancy boundary and must not be used as one.
4. **The engine splits its own state in two.** `<data-db>` holds one
   PostgreSQL table per DynamoDB table; `<data-db>_catalog` holds accounts,
   IAM, the table registry and `settings`. They differ in size, in read
   pattern and in what a restore destroys, so a control-plane read never
   contends with a tenant's scan and a tenant restore cannot take the account
   registry with it. The `connection_string` names the **catalog**, which is
   the root of discovery: `settings` carries the data database's name and
   connection string, along with the deployment's encryption key.

Deploy procedure: `INSTALL.md` § The store server.

- **`routd`** (`store/entities.go`):
  `messages` and the four read-shaped indexes one append fans out into
  (`message_folders`, `message_folder_counts`, `message_activity`,
  `message_delivery`); the thirteen registered resources — `acl`,
  `acl_membership`, `groups`, `routes`, `route_tokens`, `scheduled_tasks`,
  `secrets`, `pending_actions`, `network_rules`, `proxyd_routes`,
  `web_routes`, `installed_packages`, `github_sources`; the hot tier —
  `chats`, `sessions`, `chat_reply_state`, `chat_proactive`,
  `system_messages`, `user_profiles`, `turn_results`, `group_watchers`,
  `task_run_logs`, `turn_context`; plus `pane_sessions`, `secret_use_log`
  and `counters`.
- **`runed`** (`runed/runstore`): `spawns`, `session_log`,
  `circuit_breaker`. The three make three different partition choices — one
  constant for `spawns`, the folder for `session_log`, the folder as the
  whole coordinate for `circuit_breaker` — each written down in its own file.
- **`authd`** (`authd/authstore`): `signing_keys`, `signing_key_slot`,
  `auth_users`, `oauth_identities`, `refresh_tokens`, `refresh_families`.
- **`onbod`** (`store/entities.go`): `onboarding`, `onboarding_gates`,
  `invites`, `invite_redemptions` — declared beside the reads and writes in
  `store/onboarding.go` and `store/invites.go`.
- **`audit_log`** is in all four owners: one declaration in `audit`, created
  by whichever opener holds the owner (`audit.EnsureEntities`).

| Table                   | Key columns                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chats`                 | jid (key), agent_cursor, sticky_group, sticky_topic, is_group (routd)                                                                                                                                                                                                                                                                                                                                                                                      |
| `messages`              | chat_jid (partition) + `core.TS(timestamp)` + id (sort) — 245 pairs share one instant inside one chat on krons, so the id is in the key; sender, content, verb, source, attachments, topic, errored, is_observed, turn_id, link_context, routed_to (routd)                                                                                                                                                                                                 |
| `groups`                | folder (key), name, container_config, parent, model, cost_cap_cents_per_day, open, observe_window (routd)                                                                                                                                                                                                                                                                                                                                                  |
| `route_tokens`          | token_hash (key), jid, owner_folder, created_at, context, kind (`route` delivery bearer / `pair` identity-pairing link, spec 5/31) (routd)                                                                                                                                                                                                                                                                                                                 |
| `routes`                | id (key), seq, match, target, observe_window_messages, observe_window_chars (routd)                                                                                                                                                                                                                                                                                                                                                                        |
| `sessions`              | group_folder + topic (key), session_id, parent_topic, forked_at, observed_cursor (routd)                                                                                                                                                                                                                                                                                                                                                                   |
| `system_messages`       | id, folder, source, kind, body, created (routd)                                                                                                                                                                                                                                                                                                                                                                                                            |
| `scheduled_tasks`       | id (key), owner, chat_jid, prompt, cron, next_run, status, context_mode (routd)                                                                                                                                                                                                                                                                                                                                                                            |
| `task_run_logs`         | id (key), task_id, run_at, duration_ms, status, result, error (routd)                                                                                                                                                                                                                                                                                                                                                                                      |
| `acl`                   | principal + action + scope + params + predicate + effect (key), granted_by, granted_at (routd)                                                                                                                                                                                                                                                                                                                                                             |
| `acl_membership`        | child + parent (key), added_by, added_at — role memberships + JID→sub claims. ONE claim writer: a consented pairing redemption stamps `added_by='pairing'`, whether the link came from an agent's `issue_pairing_link` or onbod's greeting, so every claim is reachable by `unpair` (spec 5/31; routd)                                                                                                                                                     |
| `chat_reply_state`      | jid + topic (key), last_reply_id, engaged_until, engaged_folder (routd)                                                                                                                                                                                                                                                                                                                                                                                    |
| `secrets`               | scope_kind + scope_id + key (key), value (AES-256-GCM encrypted), created_at (routd)                                                                                                                                                                                                                                                                                                                                                                       |
| `secret_use_log`        | ts, spawn_id, caller_sub, folder, tool, key, scope, status, latency_ms (routd)                                                                                                                                                                                                                                                                                                                                                                             |
| `turn_context`          | turn_id (key), folder, topic, chat_jid, trigger_sender, started_at, run_id, state, gateway_key_hash — sha256 of the key the model gateway metered this turn with, the handle `GET /v1/cost` resolves (routd)                                                                                                                                                                                                                                               |
| `turn_results`          | folder + turn_id (key), session_id, status, recorded_at (routd)                                                                                                                                                                                                                                                                                                                                                                                            |
| `network_rules`         | folder + target (key), created_at, created_by — the per-folder egress allowlist (routd)                                                                                                                                                                                                                                                                                                                                                                    |
| `web_routes`            | path_prefix (key), access, redirect_to, folder, created_at (routd)                                                                                                                                                                                                                                                                                                                                                                                         |
| `group_watchers`        | observer + source (key) — the pair IS the fact, no attributes (routd)                                                                                                                                                                                                                                                                                                                                                                                      |
| `pane_sessions`         | team_id + user_id + thread_ts (key), channel_id, context_jid, opened_at (routd)                                                                                                                                                                                                                                                                                                                                                                            |
| `message_folders`       | folder (partition) + the message's own position (sort) — the folder feed: a POINTER to the chat and message, not a copy, written when `routed_to` is stamped (routd)                                                                                                                                                                                                                                                                                       |
| `message_folder_counts` | folder (key), n — one item per folder, so the usage page reads 95 items instead of 95 partitions (routd)                                                                                                                                                                                                                                                                                                                                                   |
| `message_activity`      | one constant partition, the message's own position as sort — the newest-across-all-chats read, swept at `ActivityRetention` (routd)                                                                                                                                                                                                                                                                                                                        |
| `message_delivery`      | delivery status (partition) + the message's own position (sort), attempts — `sent` IS the absence of an item (routd)                                                                                                                                                                                                                                                                                                                                       |
| `pending_actions`       | id (key) — one tool call held for a human's verdict; the hold rule is an `acl` row and the suspended call is routd's (spec 5/19; routd)                                                                                                                                                                                                                                                                                                                    |
| `proxyd_routes`         | path (key), backend, auth — proxyd's reverse-proxy table, served on its own `/v1/proxyd_routes` (routd)                                                                                                                                                                                                                                                                                                                                                    |
| `github_sources`        | source key (key), owner_folder, secret — gitd's webhook delivery sources (spec 6/27; routd)                                                                                                                                                                                                                                                                                                                                                                |
| `chat_proactive`        | jid (key) — the instant that chat last spoke unprompted, i.e. the cooldown (spec 5/6; routd)                                                                                                                                                                                                                                                                                                                                                               |
| `user_profiles`         | sub (key), the username that person's folder is named after, their per-day spend cap (routd)                                                                                                                                                                                                                                                                                                                                                               |
| `counters`              | the name of what it numbers (key), n — one number, `routes.id`, moved by `objstore.Increment`'s compare-and-swap (routd)                                                                                                                                                                                                                                                                                                                                   |
| `installed_packages`    | (folder, name) key — folder `''` = instance-wide — source, revision, manifest (JSON: asset-kind → owned identities), asset_hashes (JSON: asset-id → content hash), installed_at — one row per installed package; the record `upgrade`/`remove`/dirty-detection read (routd, spec 5/28)                                                                                                                                                                     |
| `audit_log`             | one per owner (routd, runed, authd, onbod); correlation across them is `turn_id`. The id is the sort key and IS the instant, at `core.TS`'s fixed width plus a suffix, so ordering and the page cursor are one value; the attributes are created_at, category, action, actor, actor_sub, resource, scope, surface, outcome, duration_ms, turn_id, folder, instance, request_id, source_ip. proxyd/webd/dashd own no audit table and record through routd's |
| `session_log`           | id, group_folder, session_id, started_at, ended_at, result, error, message_count (runed). The id is the instant it started at `core.TS`'s width plus a suffix, not a sequence                                                                                                                                                                                                                                                                              |
| `spawns`                | run_id (key), folder, topic, container_name, session_log_id, session_id, kind, state, outcome, exit_code, steered, created_at, started_at, ended_at (runed)                                                                                                                                                                                                                                                                                                |
| `circuit_breaker`       | folder (key), failures, last_failure — per-folder consecutive-failure count, survives restarts (runed)                                                                                                                                                                                                                                                                                                                                                     |
| `auth_users`            | user_id (key), name, created_at (authd)                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `oauth_identities`      | user_id + provider (unique), provider_sub, linked_at (authd)                                                                                                                                                                                                                                                                                                                                                                                               |
| `refresh_tokens`        | token_hash (key), family_id, sub, scope, aud, issued_at, expires_at, used_at, revoked_at (authd)                                                                                                                                                                                                                                                                                                                                                           |
| `signing_keys`          | kid (key), priv_pem, pub_pem, active, created_at, retired_at (authd)                                                                                                                                                                                                                                                                                                                                                                                       |
| `onboarding`            | jid (key), status (`awaiting_message` → `queued`/`approved`/`refused`), prompted_at, user_sub, gate, queued_at, admitted_at (onbod). Carries no credential: the link is a `kind='pair'` row in `route_tokens` (spec 5/31)                                                                                                                                                                                                                                  |
| `onboarding_gates`      | gate (key), limit_per_day, enabled (onbod)                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `invites`               | token (key), target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count (onbod)                                                                                                                                                                                                                                                                                                                                                               |

An entity has no foreign key, so nothing cascades. A group delete purges
every scope that confers on the folder or routes into it — `web_routes` and
`route_tokens` among them — explicitly, by one containment predicate, and a
table in neither of that file's lists fails its completeness test
(`store/folder_scope.go`).

`messages.source` is the canonical adapter-of-record stamped by routd's
`handleMessages`; outbound reads routd `LatestSource(jid)`. All agent
output, delegation, and escalation flow through `PutMessage` — bot rows are
`is_from_me=1 is_bot_message=1` and filtered from inbound polling. `topic`
and `routed_to` capture audit metadata. Spec: `specs/3/c-audit-log.md`.

## Guest Lifecycle

`runed` is the only daemon that starts a turn. One ephemeral KVM guest
per turn (`qemu-system-x86_64`, spec 6/41), always bound to the same
per-group folder and its own tap. The guest runs one turn and exits;
the group folder mounts at `/home/node/` so conversation state, diary,
skills, and the Claude Code session jsonl persist between turns —
context accumulates per-group because each run resumes the session
jsonl, not because the process is long-lived. Per-group web slots
(`~/public_html`, `~/private_html`) bind-mount into the unified
`<data>/web/{pub,priv}/<folder>/` trees — writes appear at both the
agent's home path and the URL-serving path. The agent reaches platform
data only over the in-process MCP socket routd hosts (no DB mount).
Sibling groups get their own guests on their own taps — the
cross-tenant boundary is the group (folder + tap + scoped token),
not the turn. Threat model + isolated axes in `SECURITY.md` § Model.

**Why per-group context isolation matters.** Each group is one agent
with one accumulating context window (carried in its session jsonl).
Sales, SRE, and recruiting never share a context. Mixing unrelated
concerns in one context causes the model to blend them: wrong answers,
hallucinated references, noise from irrelevant history. The group
boundary is the context boundary: each group keeps a clean, persistent,
focused context that accumulates only the conversations relevant to it.

routd resolves the route, derives grants, and dispatches the turn to
runed (`POST /v1/runs`). runed performs the spawn:

1. `runed.NewQemuRuntime()` (once, at start) — check every substrate
   precondition (`/dev/kvm` open, the kernel and rootfs artifacts present,
   the clamp, guest memory against the concurrency cap) and reap any guest
   a previous runed left running, from the on-disk VM record
2. `runed` renders the turn (`renderTurn`) and hands it to the substrate:
   - Resolve path via `groupfolder.Resolver`
   - `buildMounts()` → `mountsec.ValidateAdditionalMounts()`
   - `seedSettings()` — write `settings.json` (env, arizuko MCP via socat)
   - `qemuRuntime` delivers the rendered mounts, env and stdin to the guest:
     the mounts as one virtio-fs share assembled from a staging tree, the env
     layered over the release image's own, the stdin as a ctl frame
   - `seedSkills()` — copy `ant/skills/` (re-seeds every run); seed `.claude.json`
   - Env: `WEB_PREFIX` (`pub` for an elevated `/root` turn, world subdomain for children),
     `ARIZUKO_IS_ROOT`, `ARIZUKO_DELEGATE_DEPTH`, `WEB_HOST`,
     `ARIZUKO_ASSISTANT_NAME`, plus group overrides
   - boot the guest and wait for its `{exit}` frame. Per-turn results arrive
     over MCP (`submit_turn`), not stdout.
   - Output: only explicit delivery tools reach the user; assistant prose stays
     in the transcript. `submit_turn` carries the outcome and session id.
     An owed turn without a delivery receives one nudge before completion;
     ant resumes the same turn and transcript. Stop hooks ask the same hidden
     `check_turn` decision. Isolated turns never replace chat session lineage.
     The run outcome returns on the `/v1/runs` response.
   - Run timeout (`RUNED_RUN_TIMEOUT`): graceful stop then kill
   - Log: `groups/<folder>/logs/container-<ts>.log`

### Secret injection

Three credential types (spec [`5/14`](specs/5/14-credentials.md)), two
delivery paths:

- **Env-profile keys** (model creds: `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`) — pick which
  model a spawn talks to. The operator default lives in the host `.env`
  (`runed/secrets.go:readSecrets()`); a user's own key (BYOA) at user
  scope in the `secrets` table overrides it. **Spawn-inject** — merged into
  the guest env at start. This is the only table-backed credential that
  gets there, because the key is spent by the Claude Code SDK subprocess and
  there is no tool call to attach a broker to.
- **Capability credentials** (per-service tokens like `GITHUB_TOKEN`) —
  **brokered on the host at call time, never in the guest env.** The tool
  call resolves them through `routd/mcp.go:ResolveConnectorSecrets` →
  `ConnectorSecrets(folder, callerSub, required)` (user rows win over folder
  for real user triggers; timed/system use `service:routd` → folder scope),
  narrowed to the keys that call declares, spent as a header on the
  connector's HTTPS request (`ipc/connector.go`) or on the outbound REST
  request (`ipc/extcall.go`), and scrubbed from the
  result. Grant-gated. A capability row is written either by a pasted PAT
  (`/dash/me/secrets`) or via **surrogate OAuth** — the dashboard's
  "Connect GitHub" button (`/dash/me/connections`, `auth/surrogate/`, spec
  [`5/15`](specs/5/15-surrogate-oauth.md)) runs the OAuth dance and writes
  access + refresh tokens into the same `secrets` row; the broker refreshes
  near-expiry tokens at call time (`ConnectorSecrets`).
- **Infra / platform anchors** (`AUTHD_SERVICE_KEY`, `SECRETS_KEY`, bot
  credentials) — instance-wide, host `.env` only, never in the `secrets`
  table.

`mergeSecrets(anchors, userEnvProfileKeys)` builds the guest env, and
`routd/sibling_db.go:EnvProfileSecrets(userSub)` is what supplies the second
argument — a `scope_kind='user'` query filtered to the four model keys, so no
folder row and no capability credential can reach the guest. A folder secret
and a daemon env var can share a name (`GH_TOKEN` in `.env`, `GH_TOKEN` in
the folder's `secrets` rows) and hold different values; a connector reads the
folder's row through the broker and never the daemon's environment. See
`SECURITY.md § Secret injection`, [`5/14`](specs/5/14-credentials.md), and
[`5/13`](specs/5/13-ext-mcp.md).

**Model gateway** ([`6/23`](specs/6/23-llm-gateway.md)) — with the
`litellm` package installed, the model credential stops at the gateway
container: `template/services/litellm.yml` reads `ANTHROPIC_API_KEY` (or
`CLAUDE_CODE_OAUTH_TOKEN`) straight from the instance `.env`, and the guest
receives `MODEL_GATEWAY_URL` + `MODEL_GATEWAY_KEY` instead — a
virtual key litellm checks, worth nothing anywhere else. One key per TURN:
routd mints it with the run ceiling as its TTL, carrying the folder as its
team and the caller as its user, hands it to runed with the run, and deletes
it after.

**The gateway is the ledger** ([`6/39`](specs/6/39-gateway-owns-spend.md)) —
arizuko stores no spend. A folder's `cost_cap_cents_per_day` becomes its
team's `max_budget` with `budget_duration: "1d"`, and a person's becomes
their internal user's, so the cap is enforced by the thing that meters it and
the refusal happens on the request. The pre-spawn gate is one
`GET /team/info` doing three jobs: it creates the team a turn's key needs to
exist at all, converges its budget on the cap the operator set, and reads the
spend the refusal quotes. Every operator surface reads back through routd's
`/v1/cost/summary`. The package ships the Postgres litellm needs for keys and
spend (`litellm_db`), which is the gateway's bookkeeping and not the
instance's own store.

With no gateway there is no meter: the dashboard says so rather than showing
a number the agent chose, and a cap set on such an instance is enforced by
nothing, which the gate says in the log.

Session: routd mints the topic's session id when it has none and persists it
in `sessions` BEFORE dispatch (`routd/dispatch.go`). Claude opens its session
under that id (SDK `sessionId`, `ant/src/claude.ts:493`), so a turn killed
before `submit_turn` by a restart, a deadline or a crash is resumed by the next
turn in that topic (`routd/session_continuity_test.go`). Codex and pi resume an
id that names no thread, start fresh and report their own id, which
`submit_turn` then stores (BUGS `L432`). Error with no reply → the cursor stays
on the batch and the next poll re-feeds it, up to `maxTurnRetry` times and
never after a terminal failure. Error with a reply, with retries exhausted or
terminal → the messages are marked errored, the chat gets a failure notice and
the cursor advances (`runTurn`, `routd/dispatch.go`).

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
returns; runed never mounts it (spec `6/41` §"The peer credential").

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
  the guest before the closure returns true (spec `6/41` §"Steer and stop")

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
effect, grant_option)` → `auth.Authorize`. Deny wins; no matching allow
row = deny, loud. Nothing derives capability from a folder's depth.

- `auth.Authorize(caller, action, target, params)` — the single per-call
  gate (`auth/authorize.go`)
- `auth.EffectiveActions(store, caller)` — the visibility view behind
  `tools/list`: does the caller hold this action at ANY scope
- `auth.Delegate` — grant onward, bounded to a subset of rows the granter
  holds WITH GRANT OPTION (`auth/delegate.go`)

Rule grammar inside a row's `params` is `[!]action[(param=glob,...)]`.
Seeded roles: `role:member` (the messaging floor, bound to every folder at
create) and `role:operator` (`*` on `**`, WITH GRANT OPTION). Guest
capabilities are resolved from `acl` at dispatch and shipped as typed
booleans (`ShareReadOnly`/`Egress`/`WebPublish`) on the run request —
runed and container consume a decision, not a rule bundle. Model:
[`specs/5/33-paths-roles.md`](specs/5/33-paths-roles.md); row + evaluator:
[`specs/5/32-acl-unified.md`](specs/5/32-acl-unified.md).

## Operator

"Operator" is **not a role flag or sentinel** — it is membership in the
`role:operator` principal in the unified ACL. Any sub joined via
`acl_membership(<sub>, role:operator)` inherits the seeded row
`acl(role:operator, *, **, allow)` and matches every authorization check.
`auth.Authorize` handles operator visibility uniformly; there is no
`groups.is_operator` column, no `router_state['operator_jid']` sentinel,
and no nil-default routing target.

Cross-group system notifications (errors, health events, scheduled digests)
resolve their destination by querying `acl_membership` for `role:operator`
members and routing messages into their existing folders — not by seeding
a specially-flagged group.

What this means in practice: adding a user to `role:operator` is how you
make them the operator. Removing the membership demotes them. dashd is the
management UI; its sessions are scoped to users with at least one allow
row in `acl` (directly or via membership).

Open design questions for proactive-operator delivery (not yet shipped):

- Error / health-check trigger plumbing.
- Dedup / rate-limit policy for error bursts.
- Listener-digest delivery format.

These are mechanism questions; the addressing question (membership, not
flag) is settled.

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
  not via injection (spec 5/F rev6); `routd/prompt.go`
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

Platform mounts follow FHS canonical locations; per-group web slots
live under `~` in the agent's home. Full spec:
`specs/5/V-web-vhosts.md`.

| Guest                  | Host                           | Mode                                   |
| ---------------------- | ------------------------------ | -------------------------------------- |
| `/opt/arizuko`         | `<data>/app-src/`              | RO                                     |
| `/var/lib/www`         | `<data>/web/pub/`              | RO whole tree, `web:publish` grant     |
| `/var/lib/www/arizuko` | `<data>/web/pub/arizuko/`      | RO doc site, every folder, no grant    |
| `/run/ipc`             | `<data>/ipc/<folder>/`         | RW                                     |
| `/var/lib/share`       | `<data>/groups/<world>/share/` | RW always (RO via grant)               |
| `/var/lib/groups`      | `<data>/groups/`               | RW, elevated `/root` turn only         |
| `/mnt/<name>`          | operator extras                | varies                                 |
| `/home/node/`          | `<data>/groups/<folder>/`      | RW (group home)                        |
| `~/public_html`        | `<data>/web/pub/<folder>/`     | RW per-group slot, `web:publish` grant |
| `~/private_html`       | `<data>/web/priv/<folder>/`    | RW per-group slot, `web:publish` grant |

The `~/public_html` and `~/private_html` slots are bind-mount VIEWS
into the canonical web tree — writes appear simultaneously at the
home path and at the URL-serving path. `/pub/<folder>/...` serves
`web/pub/<folder>/...` (no auth); `/priv/<folder>/...` serves
`web/priv/<folder>/...` (JWT). Nested subgroup URLs preserved
(`/pub/atlas/support/...`); name-clash between a parent's own
subdir and a child subgroup is resolved by visibility through the
`/var/lib/www/` RO view.

## Mount Security (mountsec)

`ValidateAdditionalMounts` validates group-configured mounts against a
caller-supplied `Allowlist`. `ValidateFilePath` guards inbound paths (MCP
tool arguments) against symlink escapes and a blocklist (`.ssh`, `.gnupg`,
`.env`, credentials, private keys). Guest path: `/mnt/<name>`.

## Mount paths inside runed's own container

`runed` builds its mount table entirely in the paths it sees itself:
`virtiofsd` is runed's own child, in runed's own mount namespace, so a mount
source is never translated through `HOST_DATA_DIR`. `HOST_DATA_DIR` and
`HOST_APP_DIR` remain compose facts used to render the compose file and to
locate the staged `ant/` release, not inputs runed reads to rewrite a mount
source (BUGS `L368`).

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
  via `ttsd` + `TTS_BASE_URL` (`ttsd/README.md`), oracle
  skill in `ant/skills/oracle/` driving the `codex` CLI,
  model gateway (`litellm` package + `MODEL_GATEWAY_URL`,
  `routd/README.md` § The model gateway)).

Four layers sit on each other, and each boundary between them is a
contract, not a directory. The surfaces, the channel adapters, webd and
proxyd, speak the channel protocol and HTTP to the core platform: routd,
authd and the store. The core hands a turn to the execution substrate, runed
and its KVM guest, over `POST /v1/runs`. The agent, ant, speaks MCP and
`submit_turn` over the turn's socket. ant consumes the tool list resreg
renders and is a component of nothing, so a second harness is a sibling of
ant inside the guest, never of runed (`ant/README.md` § The harness).

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
theme/    shared CSS + HTML helpers (every daemon page and login page)
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
        turn (`SECURITY.md` § Network egress isolation)
anteval/  agent-capability gate: a black-box prober that runs real tasks
        through the public surfaces (REST/HTTP/MCP + a callback sink) against
        a LIVE instance and grades observable effects, not prose. Zero
        arizuko-internal imports; shippable separately. specs/5/9.
```

TTS (`ttsd/`, `ttsd/README.md`) and the oracle skill
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

proxyd is the one HTTP entry point. It authenticates the caller, stamps
`X-User-*` behind its `service:proxyd` bearer, and forwards by longest prefix
over the `proxyd_routes` rows that `arizuko generate` writes. The request path,
the row fields and the world-host redirect are drawn in
[`proxyd/README.md` § A request through proxyd](proxyd/README.md#a-request-through-proxyd).
webd is the web channel adapter behind `/chat/`, `/hook/`, `/pair/` and `/me`
([`webd/README.md`](webd/README.md)); davd serves `/dav/` through proxyd's
`davAllow` write guard.

## Auth Hardening

authd signs every platform token and every daemon verifies offline. Cookie
flags, the Telegram widget window, provider allow-lists and the one
`issueSession` path: [`authd/README.md` § Keys, mints and
logins](authd/README.md#keys-mints-and-logins).

## Discoverability

Every HTTP-serving daemon mounts `GET /openapi.json`, public and mounted
before auth. The engine emits one operation per `Endpoint` the daemon
actually mounted, with schemas reflected from `resreg.Resource.RowType`; no
path is invented from a PK-CRUD convention, and a daemon cannot advertise a
resource it does not serve. Rules: [`resreg/README.md` § OpenAPI
emission](resreg/README.md#openapi-emission). Aggregator:
`/pub/arizuko/reference/openapi.html`.

## Cold tier vs hot tier

Two management surfaces, one handler, an injected gate per surface. The
same figure heads `resreg/README.md`, which holds the rules.

<!-- twin:twofaces -->

```
┌───────────────────────────────────────────────────────────────────────────┐
│ resreg.Resource, declared once in resreg/resources/<name>.go:             │
│ Name, Table, RowType, PKFields, Endpoints, MCPDoc, MCPArgs                │
│ the daemon takes it by resreg.Mount and sets Authz, Handler, Store, Gate  │
└──────────────┬─────────────────────────────────────────────┬──────────────┘
               ▼                                             ▼
┌──────────────────────────────────┐ ┌──────────────────────────────────────┐
│ REST face: RegisterREST          │ │ MCP face: MCPTools                   │
│ /v1/<name>, one op per Endpoint; │ │ deriveMCPTools: one tool per         │
│ /openapi.json from the mounts    │ │ Endpoint that has an MCPDoc,         │
│ caller: the bearer's sub, scope  │ │ <name>.<action> or MCPNames          │
│ and folder claim                 │ │ caller: the turn's folder and sender │
│ Gate: a bearer-scope check, or   │ │ Gate: AuthorizeTurn on the target;   │
│ Authorize on <name>:<action>     │ │ routes, scheduled_tasks run it in    │
│ routes, scheduled_tasks add      │ │ containFn behind agentAllowGate      │
│ containFn: ownsFolder            │ │                                      │
└──────────────┬───────────────────┘ └──────────────────┬───────────────────┘
               └────────────────────┬───────────────────┘
                                    ▼
┌───────────────────────────────────────────────────────────────────────────┐
│ invoke: Authz ► Gate ► Handler ► one GuardedBatch with the handler's      │
│ writes and one audit_log row; a read writes no row                        │
└───────────────────────────────────────────────────────────────────────────┘
┌───────────────────────────────────────────────────────────────────────────┐
│ beside it: a forwarder (Store nil: no gate, no row here) for secrets on   │
│ routd and proxyd_routes on webd; the hot tier, MCP-only in ipc/ipc.go:    │
│ reply, send, like, inspect_*                                              │
└───────────────────────────────────────────────────────────────────────────┘
```

<!-- /twin -->

- **Cold tier** — operator config resources (`routes`, `acl`, `groups`,
  `secrets`, `scheduled_tasks`, `network_rules`, `web_routes`,
  `route_tokens`, `onboarding_gates`, `proxyd_routes`, and the rest in
  `resreg/resources/names.go`). Not every resource wears both faces:
  `secrets` is REST-only and write-only, `network_rules` is agent-only, and
  the `groups` REST twin is list-only.
- **Hot tier** — agent runtime actions (`reply`, `send`, `like`, `delete`,
  `engage`, `inspect_*`), MCP-only by design in `ipc/ipc.go`.
  `submit_turn` and `submit_status` sit below the hot tier: JSON-RPC methods
  on the same socket, absent from `tools/list`, with no REST twin and no
  resreg resource. They are delivery plumbing, not a managed entity.

Which tools a caller sees is `auth.EffectiveActions` over the same rows the
gate reads. A handler that takes a `jid`, `folder` or `run_id` binds it to
the caller's folder; `POST /v1/messages` is cross-folder by design.

Config and runtime tables follow different rules. `arizuko apply` writes
config tables only, and a runtime table is never bulk-deleted. No daemon
caches config rows in memory, so an apply is live on the next read.
Removing a group clears its routing side-channels and keeps its history.

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
  inside it, kept apart by the import-graph rule in
  `tests/components/imports_test.go`.

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

Channels are external processes that register with routd over HTTP; both sides
are HTTP servers, and every call carries an ES256 service bearer. The protocol
figure, the outbox and ledger contract, the error classes and the media path
are in [`chanlib/README.md`](chanlib/README.md#the-channel-protocol-register-health-inbound-send);
the registry, the name pin and the capability gates in
[`chanreg/README.md`](chanreg/README.md).

The protocol has two standards, not one. Ingest is a struct: `apiv1.Message`,
with `Verb` as its discriminator and `PutMessage` as its one appender. Egress is
an interface: the 14 methods of `chanlib.BotHandler`. A symmetric "connector"
primitive covering both would need a second, REST-shaped face for the same
calls.

Typing is a side rail: routd ends it when the turn ends, for every chat the run
owned, and the adapter's own TTL, 90% of `RUNED_RUN_TIMEOUT`, is the backstop
below the turn's wall. slakd and gitd draw it as a `👀` mark (`chanlib.AckMark`).

## Inbound Media Pipeline

routd downloads each attachment from the URL the adapter gave and
stores it under `groups/<folder>/media/<YYYYMMDD>/`, which the guest sees as
`/home/node/media/`, and appends one `<attachment …/>` line to the message.
teled serves `/files/<id>` as a Telegram CDN proxy, because bot-token URLs
expire; discd hands over the CDN URL directly. Rules:
[`chanlib/README.md` § Media](chanlib/README.md#media-the-transcript-is-appended-and-kept);
`MEDIA_ENABLED`, `VOICE_TRANSCRIPTION_ENABLED` and `WHISPER_*` are in
`routd/README.md` § Configuration.

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

routd serves one MCP socket per turn at `ipc/<folder>/gated.sock`; the folder
is a `ServeMCP` parameter, and the kernel-attested peer uid gates each accept.
The frame path, the tool sources, the two checks and the `submit_turn`
contract: [`ipc/README.md`](ipc/README.md).

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

There are two circuit breakers, and they count different things. routd's
`GroupQueue` counts consecutive failures per chat JID, opens at 3
(`queue/queue.go:11`) and resets on the next inbound for that chat. runed's
Manager counts failures per folder. A transport failure to runed charges
neither, and neither does a `busy` answer. The routd dashboard's retry
clears a chat's errored rows; there is no separate reset verb.

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

routd stays the policy decision point and keeps `acl` and `acl_membership`
in its own store, with no `/v1/authorize` endpoint. Both tables are read
in-process on every agent tool call and every REST mutation, so moving them
to authd would put an HTTP hop on the busiest paths.

## Operator

An operator is a member of `role:operator`, which holds the seeded row
`(*, **)` WITH GRANT OPTION. There is no operator flag and no sentinel.
`arizuko group <inst> grant <sub> '**'` adds the membership, `ungrant`
removes it, and `auth.Authorize` treats the operator like any other
principal.

## Compose Containers

`compose.Generate(dataDir)` writes `docker-compose.yml`: every core stanza,
always, plus one `include:` per package fragment in `<dataDir>/services/`.
`COMPOSE_PROFILES` in the managed `.env` block decides what docker starts.
The figure, the profile rules, the package path from catalog to container
and the multi-account variants are in [`compose/README.md`](compose/README.md).

## Onboarding (onbod/)

A chat reaches an agent only on two independent axes, and each fails closed.
Identity is the `acl_membership` edge a pairing writes; location is a `routes`
row. Routing never reads the ACL and the ACL never creates a route, so an
anonymous sender in an unrouted chat gets nothing. onbod is the self-serve
path from one to the other: greeting, pairing, admission, world choice. The
flow is drawn in [`onbod/README.md` § From an unrouted chat to a routed one](onbod/README.md#from-an-unrouted-chat-to-a-routed-one).

World, agent and session are words for folder shapes, and none is a table.
A world is a top-level folder and owns nothing of its own: its roster is the
`acl` rows on `<world>/**` and their members, its vhost is derived from the
name, and its secrets resolve by folder ancestry. A `worlds` table would be
a second path beside each of those. An agent cannot create a world: a
top-level `register_group` from the agent socket is refused with "worlds are
CLI-only". The CLI creates one. onbod creates one for a person admitted
through an enabled gate, or for a person who holds an invite to `/`.

## Scheduler (timed/)

Standalone daemon that fires due `scheduled_tasks` as messages. It
federates the fire loop over routd (`ROUTER_URL`), authenticating with a
`service:timed` token exchanged from `AUTHD_SERVICE_KEY` at `AUTHD_URL`;
routd picks the message up via normal poll. See `timed/README.md`.

## Operator Dashboard (dashd/)

Every daemon serves its own `/dash/<daemon>/` from handlers beside its
`/v1`, and proxyd routes each prefix to it. dashd is the hub of tiles at
`/dash/services/` plus the pages that span daemons. It holds a store key
for routd's and onbod's tables only and reaches every other daemon over
its `/v1`. The doors, the hub and the WHY of each are in
[`dashd/README.md`](dashd/README.md).

## Diary (diary package)

The group folder holds the agent's memory in layers: `~/work.md`, the
diary, the episodes, `~/users/`, the facts and Claude Code's own
`MEMORY.md`. runed pushes a short form of the first four into every prompt,
a user file as a pointer only; the agent pulls the facts on demand. The layers,
the windows, the one nudge and the compaction tasks are in
[`diary/README.md`](diary/README.md).

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

A turn has one deadline, `RUNED_RUN_TIMEOUT` (default 20m), and the agent's
query timeout sits 30 seconds under it, so the harness reports before runed
kills the guest. The derivation, what the user sees and the mitigations for
skill authors are in [`ant/README.md`](ant/README.md) § The turn deadline.

## Prompt Assembly

routd builds the turn's message string, runed prefixes the knowledge layers
it reads off the group folder, and ant renders the system prompt inside the
guest. The figure and the table of every block, its builder and when it is
emitted are in [`routd/README.md`](routd/README.md) § Prompt assembly.

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

A daemon mounts only the subpaths it opens (`compose/compose_test.go`
`wantDataMounts`), because a mount of the whole tree hands it every other
owner's files and every live MCP socket under `ipc/`. routd and runed take
the whole tree: routd reads `connectors.toml`, `surrogate/*.toml` and
`tts/` from the root and creates `audit-*.jl` there, and runed drives the
spawn path. dashd mounts `groups/` and `surrogate/`, onbod `groups/` and
`web/`; authd, webd and proxyd mount nothing.

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

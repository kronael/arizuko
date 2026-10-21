# Architecture

Where each piece of arizuko lives, which daemon owns which table, and how a
message becomes a reply. What arizuko is and how to start: [README](README.md).
How to add a channel, a tool or a skill: [EXTENDING](EXTENDING.md). Which
folder answers a chat: [ROUTING](ROUTING.md). The threat model:
[SECURITY](SECURITY.md).

## Where a change lands

A change touches the layers from the bottom up: the shared ID in `types/`, the
row shape in `core/`, the entity in the owner's store package, the resource in
`resreg/resources/`, the owning daemon's handler, and last its `api/v1/`, the
only package another daemon imports. `tests/components/imports_test.go` fails
on an upward edge.

```
types/ obs/ core/  layer 0: shared IDs, telemetry, config and row types
audit/ auth/       layer 1: the audit row; token verify, the acl evaluator
resreg/ chanlib/   layer 2: resource registry; the adapter library
store/ objstore/   owner entities; Query and GuardedBatch, dynstore

routd/     router plane: sole message appender, routing, prompt, MCP host
  │
  ├── router, chanreg     formatting and rules; registry and delivery
  ├── queue, proactive    one turn per folder; the silence scanner
  └── ipc                 the per-turn agent MCP socket, in-process
runed/     execution plane: one KVM guest and one egress proxy per turn
  │
  ├── container, diary    group setup and skills; the knowledge layers
  ├── groupfolder, mountsec  path checks for the guest's mounts
  ├── substrate           the declared ceiling and what a host delivers
  └── runstore            spawns, session_log, circuit_breaker
kernel/    the pinned guest kernel and guest-init, PID 1
authd/     auth authority: sole ES256 signer, JWKS, OAuth provider
  │
  └── authstore           signing keys, users, refresh tokens

proxyd/ proxycfg/   the one HTTP entry; its routes as a Caddyfile
webd/ davd/ vited/  web chat adapter; WebDAV; the static origin
dashd/ onbod/ timed/  cockpit hub; onboarding; the scheduler
compose/ cmd/ theme/  compose generation; the CLI; shared page CSS
teled/ discd/ slakd/ bskyd/ reditd/ emaid/ whapd/ twitd/ linkd/ gitd/
googd/              channel adapters, one daemon per platform
egress/ substrate/ mcpfw/  shippable components, no arizuko imports
ttsd/ sidecar/      speech synthesis; the whisper-cpp image
ant/ anteval/       the agent harness and skills; the capability gate
template/ tests/    fragments, manifests, web docs; cross-package tests
```

## Core vs integrations

The core runs with no channel adapter: a person reaches it through the web
chat or `arizuko talk`, an MCP client over proxyd, and that personal topology
runs the same code as a deployment with every adapter. An integration plugs
in per deployment: a channel adapter, the `litellm` model gateway, speech, or
a host tool whose only surface is a skill (`EXTENDING.md` § Host-tool
capabilities). Four layers sit on each other, each boundary a contract: the
surfaces speak the channel protocol and HTTP to the core, the core hands a
turn to runed over `POST /v1/runs`, and ant speaks MCP inside the guest. A
second harness is a sibling of ant, never of runed (`ant/README.md` § The
harness).

## The three planes

Each plane is one daemon that owns its own tables. The planes talk over HTTP
`/v1/*` and verify each other's tokens offline against authd's JWKS.

- **`routd`** appends every message, resolves routes, builds the prompt,
  serves the per-turn MCP socket and dispatches the turn. It verifies tokens
  and signs none. See [`routd/README.md`](routd/README.md).
- **`runed`** alone holds `/dev/kvm`, `/dev/vhost-vsock` and `/dev/net/tun`.
  It boots one guest and one egress proxy per turn; the guest's vsock CID is
  the turn's credential. See [`runed/README.md`](runed/README.md).
- **`authd`** is the sole ES256 signer, the JWKS publisher and the OAuth
  provider, see [`authd/README.md`](authd/README.md). The `auth/` library is the
  offline verifier every other daemon links.

## Data ownership

Every table is an objstore entity at a `(partition, sort)` coordinate. One
DynamoDB-protocol server, named by `DYNAMODB_URL`, holds every owner's tables
as `<owner>.<entity>`. The layers from a daemon down to PostgreSQL, and why
the store has that shape: [`objstore/README.md`](objstore/README.md).

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
timed, ttsd, googd, whapd and twitd hold no store key; timed and ttsd call an owner's /v1
```

| Owner                                      | Entities                                                                                                   | Declared in                            | Column detail                                |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------------- | -------------------------------------- | -------------------------------------------- |
| `routd`                                    | messages and their four indexes, routing, grants, secrets, sessions, turns, tasks, packages                | `store/entities.go`                    | `store/README.md` § Every table is an entity |
| `onbod`                                    | `onboarding`, `onboarding_gates`, `invites`, `invite_redemptions`                                          | `store/entities.go`                    | `store/README.md` § Every table is an entity |
| `runed`                                    | `spawns`, `session_log`, `circuit_breaker`                                                                 | `runed/runstore`                       | `runed/README.md` § Entities owned           |
| `authd`                                    | `signing_keys`, `signing_key_slot`, `auth_users`, `oauth_identities`, `refresh_tokens`, `refresh_families` | `authd/authstore`                      | `authd/README.md` § Entities owned           |
| each adapter that calls `OpenAdapterStore` | `outbound_dedup`, `inbound_pending`, `file_urls`, `ack_marks`; emaid adds `email_threads`, `email_msg_ids` | `chanlib/chanstore`, `emaid/emaistore` | `chanlib/README.md` § Storage                |

- **`audit_log` is in all four owners**, declared once in `audit` and
  correlated by `turn_id`. proxyd, webd and dashd record through routd's.
- **Each second opener of routd's tables has one reason**: onbod and dashd
  hold operator writes, webd resolves a route token, gitd reads a webhook
  secret, slakd reads the pane, proxyd is the hot-path exception below.
- **No foreign key, so nothing cascades.** A group delete purges by one
  containment predicate; a table missing from `store/folder_scope.go`'s
  lists fails its completeness test.

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

- One `messages` table, one decision point: a delegation, an escalation and
  a `#topic` re-post are rows the router resolves like any inbound. Ingest
  and the `pollOnce` backstop converge on `processGroupMessages`, so a slash
  command cannot race past the steer step.
- The `acl` rows become four typed booleans on the run request
  (`ShareReadOnly`, `Egress`, `WebPublish`, `GHWrite`). runed consumes a
  decision and never reads a grant.

Each step and its failure handling: `routd/README.md` § The turn in routd,
`runed/README.md` § The turn envelope, `ROUTING.md` § Resolution Order;
for a newcomer, `concepts/turn.html`.

## Cold tier vs hot tier

Two management surfaces run one handler, each behind its own injected gate.
The figure and the rules head
[`resreg/README.md` § One handler, two faces](resreg/README.md#one-handler-two-faces).

The cold tier is every name in `resreg/resources/names.go`; `secrets` is
REST-only, `network_rules` agent-only, the `groups` REST twin list-only. The
hot tier is MCP-only. `submit_turn` and `submit_status` sit below it, hidden
from `tools/list`. `arizuko apply` writes config tables only, and no daemon
caches a config row, so an apply is live on the next read. Nothing
bulk-deletes a runtime table: a removed group keeps its history.

## Daemon conventions

- **Naming.** A daemon ends in `d`: a process that owns at most one set of
  tables and serves REST, MCP or both. A library does not end in `d` and is
  imported, never run. One Go module holds both.
- **Layers.** The layer rule heads the map above. Layer 3 is the daemons,
  which import layers 0-2 and other daemons' `api/v1/`. `types/` holds the
  cross-boundary IDs as named types with no methods, which breaks the cycle
  between each `api/v1/` and `core/`.
- **`<daemon>/api/v1/` is the contract**: wire types and a thin HTTP client,
  importing nothing beyond `types/`. The daemon's handlers stay private, and
  `v2/` sits beside `v1/` when the shape breaks.
- **Component contract.** `egress`, `substrate` and `mcpfw` ship alone and
  import no arizuko package except each other (`imports_test.go`
  `shippable`). arizuko keeps the domain: ids, the folder tree, grants, when
  to spawn. A component keeps the mechanism and takes strings. One
  convenient `import core` would end that with no error, so the test fails.
- **Table ownership.** Each owner creates its own tables at boot through
  `store.PrepareOwner`, and nobody else's. A library owns no table; one that
  records something hands the caller a write for the caller's own batch
  (`audit.WriteFor`). Cross-daemon data goes through the owner's `api/v1/`.
- **The hot-path exception.** Only a per-request serving path may read
  another owner's tables directly, and `proxyd` is the one case: the
  route-token lookup and the `acl` read behind `X-User-Groups`. A login-time
  snapshot would keep a revoked grant working for the token's 15 minutes
  (`proxyd/hotpath_read_test.go`). A new direct read names its path.
- **One-shot cutovers.** A migration ships in one release and deletes the old
  path in that release. Recovery is `git revert`, never a dual-version period.

## Decided, and why

Each row is a decision the code holds today, the alternative it rejected,
and the reason. A change that reverses a row answers the reason first.

| Decided                                                                                                                                                                                                                                 | Rejected                                                              | Why                                                                                                                                                                                                                                                                                                                                                                |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| The frame is four verbs over two nouns. Ingest (`PutMessage`), Route (`Loop.resolve`), Grant (`auth.Authorize`), Meter (audit rows, the capability snapshot at dispatch, the gateway budget); the folder and the turn.                  | A new primitive per feature: a connector, a workflow, a control plane | Everything else is a service, an install-time write face or a read face of Meter. The folder is the file tree, the tenancy key and the grant subject `folder:<path>`; the turn is the transaction.                                                                                                                                                                 |
| Ingest is a struct, egress is an interface. Inbound is one `apiv1.Message` with a `Verb` field and one appender; outbound is the 14-method `chanlib.BotHandler`.                                                                        | One symmetric connector type for both directions                      | A symmetric connector would rebuild the REST twin beside the MCP face.                                                                                                                                                                                                                                                                                             |
| Outbound has two kinds. A message is a `messages` row first and is retried from the row; an action (like, edit, pin, delete) writes no row and records an audit event (`routd/social_audit.go`).                                        | Record an action as a message row                                     | An action changes a message already on the platform; it adds nothing to the conversation, so the audit row is its only trace.                                                                                                                                                                                                                                      |
| A turn's capabilities freeze at dispatch. `dispatchRun` resolves the session, model, egress allowlist, secrets and the mount, egress, web and `gh:write` grants into one `RunRequest`; tool calls are gated per call.                   | Re-check a mount or egress grant inside a live guest                  | A credential never outlives a turn. A mount, network or secret grant change applies at the next spawn; a tool grant applies at the next call.                                                                                                                                                                                                                      |
| One substrate: a KVM guest per turn, with no selector.                                                                                                                                                                                  | A container per turn, or a selectable substrate                       | A compromised guest faces a VM boundary, not a shared container kernel (`SECURITY.md`).                                                                                                                                                                                                                                                                            |
| No warm pool; every turn boots a fresh guest.                                                                                                                                                                                           | A pool of booted guests                                               | A guest's identity is its mount set, fixed in the QEMU argument list, so a generic warm guest can never become a folder's guest. A pooled sandbox lives for days and keeps what the agent wrote; a turn guest keeps nothing but the folder. The reopen gate is in `runed/README.md` § Boot timing.                                                                 |
| No durable workflow engine; the recovery unit is the turn.                                                                                                                                                                              | A checkpoint-and-replay engine under every turn                       | Replay assumes deterministic orchestration code, and here the orchestrator is a model. A failed turn retries whole; sleep is `timed`; a wait is a route; child work is `delegate_group` or `escalate_group`; durability is store rows plus the folder. Reopens for a multi-hour job with side effects that cannot run twice, as its own daemon with its own store. |
| No database layer: `objstore` is two methods over the DynamoDB protocol.                                                                                                                                                                | SQLite files per daemon, or a SQL layer                               | `objstore/README.md` § Why the store has this shape.                                                                                                                                                                                                                                                                                                               |
| The store is authoritative; YAML is a dump and restore format.                                                                                                                                                                          | DB-to-YAML sync, apply at startup, reload on SIGHUP                   | With one direction there is no drift to detect; a dump never claims to be live.                                                                                                                                                                                                                                                                                    |
| `acl` and `acl_membership` stay in routd, which is the policy decision point.                                                                                                                                                           | Move them to authd behind a `/v1/authorize` endpoint                  | routd reads `acl` in-process on every agent tool call and every REST mutation; a move puts an HTTP hop on the busiest paths.                                                                                                                                                                                                                                       |
| A path carries no authority; there are no tiers. Root is a grant, `role:member` is the one default role, and no tier is emitted to the agent.                                                                                           | Capability derived from folder depth                                  | A depth-derived scalar gave two-segment sub-groups unconstrained egress.                                                                                                                                                                                                                                                                                           |
| arizuko does not run `mcpfw`.                                                                                                                                                                                                           | `mcpfw` in front of the agent socket                                  | Two gates on one call drift, and a `{tool, action}` rule cannot carry folder containment or parameters (`mcpfw/README.md`).                                                                                                                                                                                                                                        |
| No control chat and no `CONTROL_JID`. An operator runs commands from any chat they own; failures surface in journald and dashd.                                                                                                         | A dedicated control JID and a notifier library                        | A dedicated JID is a second routing path, a second place for auth to drift, and a single point of failure when something is broken.                                                                                                                                                                                                                                |
| Self-learning is not built; `/learn` is the one consolidation path, run by hand.                                                                                                                                                        | Agent-written proposals, a learning mode, a curator task              | Every cold-tier entity must be a resreg resource and resreg panics on an empty `Table`, so a file-backed proposals queue cannot be one; and "the platform stays mechanical" is unsettled.                                                                                                                                                                          |
| LiteLLM is the model gateway.                                                                                                                                                                                                           | Bifrost                                                               | A subscription token must go out as `Authorization: Bearer` with the OAuth beta header; Bifrost writes `x-api-key` only (recorded at selection, not re-measured).                                                                                                                                                                                                  |
| Two published environments: the agent environment (the event, the tools, the home) and the component environment (what a daemon gets and owes). Conformance is measured: `anteval` per harness, `arizuko probe` against a live process. | A declared compatibility list                                         | A declaration cannot fail; a probe against a live instance can.                                                                                                                                                                                                                                                                                                    |
| The folder supplies eight services to any harness that occupies it: keys, egress, identity and authorization, tenant isolation, cost, audit, routing and the web host.                                                                  | Leave them to each harness                                            | A bare harness has none of the eight, so a foreign harness gets all of them by running in a folder.                                                                                                                                                                                                                                                                |
| Not adopted from Go 1.27: the `encoding/json/v2` API, `goroutineleak`, `synctest`, `AsType`, `reflect.Type.Fields`.                                                                                                                     | Adopt each with the toolchain                                         | No measured gain; `json/v2`'s strict defaults change behaviour on adapter payloads.                                                                                                                                                                                                                                                                                |
| One channel adapter daemon per platform.                                                                                                                                                                                                | One process for every platform                                        | A platform's failure, secrets, deploy and reconnect loop stay its own, and its quirks stay inside its adapter. An adapter without routd is inert, so an adapter alone is not a shippable unit.                                                                                                                                                                     |
| One context per group; the group, not the turn, is the boundary between tenants.                                                                                                                                                        | One agent context for several concerns                                | Unrelated concerns in one context make the model blend them, so sales and on-call never share one. Each turn resumes the group's session, so the context grows per group.                                                                                                                                                                                          |

## Where the rest lives

| Concern                                            | Home                                                                                 |
| -------------------------------------------------- | ------------------------------------------------------------------------------------ |
| A request through proxyd, the web channel          | [`proxyd/README.md`](proxyd/README.md), [`webd/README.md`](webd/README.md)           |
| Keys, mints, cookies and logins                    | [`authd/README.md` § Keys, mints and logins](authd/README.md#keys-mints-and-logins)  |
| `GET /openapi.json` on every daemon                | [`resreg/README.md` § OpenAPI emission](resreg/README.md#openapi-emission)           |
| The channel protocol, typing, inbound media        | [`chanlib/README.md`](chanlib/README.md), [`chanreg/README.md`](chanreg/README.md)   |
| Authorization, the operator role                   | [`auth/README.md`](auth/README.md), [GRANTS.md](GRANTS.md)                           |
| The turn envelope, steer, stop, the guest's mounts | [`runed/README.md`](runed/README.md)                                                 |
| Secret injection                                   | [`SECURITY.md` § Secret injection](SECURITY.md#secret-injection)                     |
| The MCP socket and `submit_turn`                   | [`ipc/README.md`](ipc/README.md)                                                     |
| One turn per folder, the two circuit breakers      | [`queue/README.md`](queue/README.md)                                                 |
| Routes, topics, replies, autoviv                   | [ROUTING.md](ROUTING.md)                                                             |
| Compose, profiles, packages                        | [`compose/README.md`](compose/README.md)                                             |
| Onboarding, pairing, worlds                        | [`onbod/README.md`](onbod/README.md)                                                 |
| The scheduler                                      | [`timed/README.md`](timed/README.md)                                                 |
| The dashboards                                     | [`dashd/README.md`](dashd/README.md)                                                 |
| The agent's memory layers                          | [`diary/README.md`](diary/README.md)                                                 |
| Prompt assembly                                    | [`routd/README.md` § Prompt assembly](routd/README.md#prompt-assembly)               |
| The turn deadline                                  | [`ant/README.md` § The turn deadline](ant/README.md#the-turn-deadline)               |
| Audit, logs, traces                                | [`obs/README.md`](obs/README.md), [`audit/README.md`](audit/README.md)               |
| Row types and the config loader                    | [`core/README.md`](core/README.md)                                                   |
| The guest kernel; what a substrate declares        | [`kernel/README.md`](kernel/README.md), [`substrate/README.md`](substrate/README.md) |

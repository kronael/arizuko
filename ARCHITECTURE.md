# Architecture

Where each daemon runs, how daemons communicate, where state lives, and which
boundaries an engineer must preserve. Start with [README.md](README.md) for the
glossary, [INSTALL.md](INSTALL.md) for deployment, [ROUTING.md](ROUTING.md) for
route semantics, and [SECURITY.md](SECURITY.md) for the threat model.
Code references describe v0.75.9 at `372f0b9b93c13e7f6dbdb874e93ed0d65ec336cc`.

## Where a change lands

`routd` owns conversation decisions. `runed` owns guest execution. `authd`
owns signing keys and token APIs. HTTP joins these three planes. An adapter converts its
platform's events and outbound verbs into the channel protocol.

```text
┌──────────────────────────────────────────────────────────────┐
│ Channels: teled discd slakd emaid whapd gitd googd webd      │
└───────────────────────────────┬──────────────────────────────┘
                                │ HTTP: ingest down; delivery up
┌───────────────────────────────▼──────────────────────────────┐
│ routd: messages, routing, prompt, scheduler, turn control    │
└───────────────────────────────┬──────────────────────────────┘
                                │ HTTP POST /v1/runs
┌───────────────────────────────▼──────────────────────────────┐
│ runed: guest lifecycle; ant harness; per-turn egress         │
└──────────────────────────────────────────────────────────────┘
authd: service/user tokens and verification keys for core/channel APIs.
proxyd: entry for webd, dashd, onbod, davd and daemon dashboards.
ttsd: speech backend. arizuko CLI: operator rows and files.
All store clients: signed requests to the external extenddb server.
```

The repository map below names the shared layers. Daemon directories appear
in the next section. Imports between daemons use their `api/v1/` contracts.
The component import test enforces the separate dependency boundary around
`egress/`, `substrate/` and `mcpfw/`.

| Path                                                      | Responsibility                                                              |
| --------------------------------------------------------- | --------------------------------------------------------------------------- |
| `types/`, `core/`, `obs/`                                 | Shared identifiers, row/config types, logs, metrics and traces.             |
| `objstore/`, `objstore/dynstore/`, `store/`, `audit/`     | Storage port, DynamoDB driver, domain operations and audit writes.          |
| `auth/`, `auth/surrogate/`                                | Token verification, ACL evaluation and credential brokerage.                |
| `resreg/`, `resreg/resources/`                            | Resource handlers, schemas, MCP projections and OpenAPI.                    |
| `router/`, `chanlib/`, `chanreg/`, `ipc/`                 | Prompt formatting, adapter protocol, delivery registry and agent MCP.       |
| `container/`, `groupfolder/`, `mountsec/`, `diary/`       | Package assets, folder resolution, mount checks and memory input.           |
| `kernel/`, `substrate/`, `egress/`                        | Guest kernel/init, substrate capability checks and outbound proxy.          |
| `compose/`, `template/services/`, `packages/`             | Deployment generator, bundled service packages and product package sources. |
| `ant/`, `anteval/`, `tests/`, `template/web/pub/arizuko/` | Harness runtime, capability checks, cross-package tests and operator docs.  |

Sources: [component import rules](tests/components/imports_test.go#L1),
[run request](runed/api/v1/types.go#L1), [shared rows](core/types.go#L1),
[resource registry](resreg/resreg.go#L201-L232).

## Each process has one responsibility

Addresses below are Compose service addresses. HTTP uses plain HTTP inside
the instance network. Public TLS ends at the front proxy.
The `StoreReach` column lists every store prefix a process can open, not just
its own tables. The data table below expands each prefix into entities.
`ttsd` uses `:8880`; the other deployed HTTP services below use `:8080`.
Standalone defaults also differ for `teled`, `discd` and `emaid`.

| Process and detail                   | Owns                                                                                   | StoreReach prefixes             | Callers and transport; source                                                                                                                                                        |
| ------------------------------------ | -------------------------------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [authd](authd/README.md)             | Signing keys, users, OAuth logins and refresh families.                                | `authd.*`                       | Daemons exchange service keys; browsers log in through proxyd; HTTP `http://authd:8080`. [entry](authd/main.go#L26), [handlers](authd/http.go#L75).                                  |
| [dashd](dashd/README.md)             | Operator UI and management forms; creates no owner namespace.                          | `routd.*`, `onbod.*`            | proxyd forwards `/dash/`; HTTP `http://dashd:8080`; reads sibling status via HTTP. [entry](dashd/main.go#L160).                                                                      |
| [davd](davd/README.md)               | WebDAV access to `<data_dir>/groups/`; `dufs` process, no store tables.                | No store key.                   | proxyd authenticates and filters `/dav/`, then forwards to `http://davd:8080`. [generator](compose/compose.go#L1283), [image](davd/Dockerfile#L1).                                   |
| [discd](discd/README.md)             | Discord events, sends, retained input and deduplication.                               | `discd.*`                       | Discord gateway input; routd calls outbound HTTP `http://discd:8080`. [entry](discd/main.go#L23), [fragment](template/services/discd/discd.yml#L7).                                  |
| [emaid](emaid/README.md)             | IMAP ingest, SMTP delivery and email thread mappings.                                  | `emaid.*`                       | Mail servers supply input; routd calls `http://emaid:8080`. [entry](emaid/main.go#L31), [fragment](template/services/emaid/emaid.yml#L6).                                            |
| [gitd](gitd/README.md)               | GitHub webhook ingest and API actions.                                                 | `gitd.*`, `routd.*`             | GitHub calls through proxyd; routd calls `http://gitd:8080`; reads webhook sources/secrets directly. [entry](gitd/main.go#L20).                                                      |
| [googd](googd/README.md)             | Google Drive polling, file changes and Google API forwarding.                          | `googd.*`, `routd.*`            | Polls Google; routd calls `http://googd:8080`; reads Drive bindings directly. [entry](googd/main.go#L25).                                                                            |
| [onbod](onbod/README.md)             | Admission queue, gates, invites and redemptions.                                       | `onbod.*`, `routd.*`            | routd calls its API; proxyd forwards browser requests to `http://onbod:8080`; admission writes group/policy rows. [entry](onbod/main.go#L64).                                        |
| [proxyd](proxyd/README.md)           | HTTP routing, identity stamping, static files and WebDAV gates.                        | `routd.*`                       | Browsers, webhooks and external HTTP clients enter `http://proxyd:8080`; forwards by service name. [entry](proxyd/main.go#L1094), [dispatch](proxyd/main.go#L606).                   |
| [routd](routd/README.md)             | Messages, routes, policy, prompt, scheduler, MCP and turn control.                     | `routd.*`                       | Adapters/CLI call `http://routd:8080`; ant calls the per-turn MCP Unix socket. [entry](routd/cmd/routd/main.go#L33).                                                                 |
| [runed](runed/README.md)             | Spawn records, session log, guest lifecycle and per-turn egress.                       | `runed.*`                       | routd calls `http://runed:8080/v1/runs`; operator surfaces read/stop runs over HTTP. [entry](runed/cmd/runed/main.go#L1).                                                            |
| [slakd](slakd/README.md)             | Slack events, messages and assistant panes.                                            | `slakd.*`, `routd.*`            | Slack calls through proxyd; routd calls `http://slakd:8080`; reads pane state directly. [entry](slakd/main.go#L24).                                                                  |
| [teled](teled/README.md)             | Telegram polling, sends and update offset.                                             | `teled.*`                       | Telegram API supplies input; routd calls `http://teled:8080`. [entry](teled/main.go#L40), [fragment](template/services/teled/teled.yml#L6).                                          |
| `timed`                              | Scheduled tasks run inside routd; there is no separate process, endpoint or namespace. | Uses `routd.*`.                 | `ScheduleLoop` calls routd's ingest function in-process. [scheduler](routd/schedule.go#L19).                                                                                         |
| [ttsd](ttsd/README.md)               | OpenAI-compatible speech proxy and backend health.                                     | Empty reach.                    | routd sends speech requests to `http://ttsd:8880/v1/audio/speech`; ttsd forwards to `http://kokoro:8880`. [entry](ttsd/main.go#L33), [fragment](template/services/ttsd/ttsd.yml#L6). |
| [webd](webd/README.md)               | Web chat, SSE, route-token chat/hooks and the HTTP MCP surface.                        | `routd.*`                       | proxyd forwards to `http://webd:8080`; routd delivers through its channel API. [entry](webd/main.go#L34).                                                                            |
| [whapd](whapd/README.md)             | WhatsApp session, retained input and delivery ledgers in files.                        | No StoreReach entry/key.        | Baileys receives WhatsApp events; routd calls `http://whapd:8080`; dashd proxies pairing controls. [entry](whapd/src/main.ts#L45), [fragment](template/services/whapd/whapd.yml#L8). |
| [egress](egress/README.md)           | One turn's HTTP/CONNECT allowlist and client-IP check.                                 | No store.                       | Guest uses its tap's host address, default port `3128`; runed starts/kills the process, outside Compose service discovery. [launch](runed/vmnet.go#L231).                            |
| [arizuko CLI](cmd/arizuko/README.md) | Instance files, package installation and operator row mutations.                       | `authd.*`, `onbod.*`, `routd.*` | Host operator invokes the binary; local store access and HTTP to daemons. [commands](cmd/arizuko/main.go#L34), [reach](store/owner_reach.go#L34).                                    |
| [ant](ant/README.md)                 | Prompt execution, harness sessions, hooks and outcome submission.                      | No direct store access.         | guest-init starts ant with JSON stdin; Claude, Codex or pi uses MCP at `/run/ipc/gated.sock`. [runtime](ant/src/index.ts#L16), [harness selection](ant/src/backend.ts#L137).         |

Sources: [complete store reach](store/owner_reach.go#L18-L34),
[Compose services](compose/compose.go#L861-L875),
[standalone ports](teled/main.go#L102), [Discord port](discd/main.go#L97),
[email port](emaid/main.go#L80), [public entry](compose/compose.go#L1005).

## Data ownership

The deployment uses one external `extenddb` server per host, with a DynamoDB
API over PostgreSQL. Each instance has its own account, `arizuko_<instance>`.
The server owns PostgreSQL data, its catalog, TLS files and administrator
credential. Instance Compose projects do not start the store server.
The server implementation lives outside this repository; enrollment and the
signed access tests specify the client contract.

```text
┌────────────────────────────────────────────────────────────┐
│ Daemon: <data_dir>/env/<daemon>.env key                    │
│ CLI: <data_dir>/env/operator.env key                       │
└──────────────────────────────┬─────────────────────────────┘
                               │ domain store calls
┌──────────────────────────────▼─────────────────────────────┐
│ Domain operations over objstore: Query / GuardedBatch      │
│ objstore/dynstore: owner prefix, signed DynamoDB API       │
└──────────────────────────────┬─────────────────────────────┘
                               │ TLS
┌──────────────────────────────▼─────────────────────────────┐
│ extenddb: account arizuko_<instance>                       │
│ Server policies restrict keys to owner prefixes            │
│ PostgreSQL holds <owner>.<entity> tables                   │
└────────────────────────────────────────────────────────────┘
```

`objstore.Store` has two methods. `Query` returns the complete selection up to
its limit, including driver-managed pagination. `GuardedBatch` commits all
writes only when all checks hold. A refused check returns `committed=false`
without an error. Items have partition/sort keys and string attributes.
Versioned reads, replacement and counters are protocols over those methods.
`dynstore` implements the port with the DynamoDB SDK and adds the owner prefix.
Schema preparation is driver-specific, outside the port.

The server enforces IAM-style policies. Enrollment grants `dynamodb:*` only
on `arn:aws:dynamodb:*:*:table/<owner>.*` for the prefixes in `StoreReach`.
These policies grant operations within each allowed prefix; they do not
restrict a foreign opener to read-only operations.
`arizuko store enroll -i <instance>` uses `EXTENDDB_PASSWORD` as the server's
`admin` credential through `/management`. It provisions every nonempty reach
row and writes credentials to `<data_dir>/env/<daemon>.env` with mode `0600`.
The CLI row becomes user `operator` and `<data_dir>/env/operator.env`.
The administrator password stays outside the instance.

`DYNAMODB_URL`, `AWS_REGION` and the base64 PEM certificate `STORE_CA` identify
the server. `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` identify the caller.
Generation validates HTTPS, CA and required pairs. The driver signs requests
and trusts the configured CA alongside system roots. Each HTTP attempt has a
30-second timeout. Store credentials and authd service credentials are separate.

| Prefix                                                          | Authoritative entities and consumers                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| --------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `routd.*`                                                       | `messages`, `message_folders`, `message_folder_counts`, `message_activity`, `message_delivery`; `chats`, `sessions`, `chat_reply_state`, `system_messages`, `user_profiles`; `turn_context`, `turn_archive`, `run_turns`, `turn_results`, `turn_control`, `model_pauses`; `groups`, `routes`, `route_tokens`, `acl`, `acl_membership`; `scheduled_tasks`, `task_run_logs`, `pending_actions`, `group_watchers`; `secrets`, `secret_use_log`, `network_rules`, `proxyd_routes`, `web_routes`, `installed_packages`, `github_sources`, `gdrive_files`, `pane_sessions`, `counters`. Foreign consumers are the reach rows above. |
| `onbod.*`                                                       | `onboarding`, `onboarding_gates`, `invites`, `invite_redemptions`; dashd and the CLI also open these tables.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `runed.*`                                                       | `spawns`, `session_log`; other processes use runed's HTTP API.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `authd.*`                                                       | `signing_keys`, `signing_key_slot`, `auth_users`, `oauth_identities`, `refresh_tokens`, `refresh_families`; the CLI also opens these tables.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `teled.*`, `discd.*`, `slakd.*`, `gitd.*`, `googd.*`, `emaid.*` | Each holds `outbound_dedup`, `inbound_pending`, `file_urls`, `ack_marks`. `googd.*` adds `file_poll`; `emaid.*` adds `email_threads`, `email_msg_ids`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |

The four core owners also hold `audit_log`. `routd` and `onbod` hold
`resreg_scopes` version markers. `PrepareOwner` creates an owner's declared
entities at boot. `OpenOwner` probes another owner's existing schema and
creates nothing. Libraries contribute declarations or writes, not namespaces.
A group removal purges scoped rows and files through an explicit containment
walk; there are no database cascades.

Sources: [entity declarations](store/entities.go#L17-L173),
[owner preparation](store/served.go#L15-L60), [version markers](store/scopes.go#L219),
[runed entities](runed/runstore/runstore.go#L18-L29),
[authd entities](authd/authstore/authstore.go#L25-L48),
[adapter entities](chanlib/chanstore/chanstore.go#L34-L54),
[email entities](emaid/emaistore/emaistore.go#L22-L33),
[Drive entity](googd/main.go#L42-L58), [port](objstore/objstore.go#L23-L38),
[driver](objstore/dynstore/dynstore.go#L73-L126),
[enrollment](cmd/arizuko/store.go#L113-L244),
[management client](cmd/arizuko/extenddb.go#L34-L64),
[store validation](compose/compose.go#L704-L728),
[policy tests](cmd/arizuko/store_test.go#L698),
[group removal](cmd/arizuko/group_rm.go#L48-L146).
Server setup and backups: [INSTALL.md](INSTALL.md#the-store-server-holds-every-table-of-the-instance).

## Message Flow

An adapter registers its delivery URL, JID prefixes and supported verbs with
routd. The channel registry is in memory; adapters register again after a
restart. Ingest verifies the channel, normalizes the event, appends through
`PutMessage`, then calls `Enqueue`. The store survives a lost enqueue.

<!-- twin:turn -->

```
┌─────────┐ POST /v1/messages ┌──────────────────────────────────────────┐
│ adapter ├──────────────────►│ routd: PutMessage appends the row        │
└─────────┘                   │ Enqueue; every writer wakes its chat,    │
     ▲                        │ a startup sweep the rest                 │
     │                        └────────────────────┬─────────────────────┘
     │                                             ▼
     │                        ┌──────────────────────────────────────────┐
     │                        │ turn control: one turn per folder,       │
     │                        │ agent limit, instance breaker, retries   │
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
     │                        │ runed Manager.Run: a live folder or a    │
     │                        │ full cap answers busy, else it claims    │
     │                        │ the spawns row                           │
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
     │ the reply rows and     │ bridge ► routd socket: mcpDeliver ►      │
     │ one edited status row  │ chanDeliverer.Send; submit_turn ►        │
     └────────────────────────┤ recordTurnResultAttempt (outcome only)   │
                              └──────────────────────────────────────────┘
```

<!-- /twin -->

Routing uses direct folder targets, ordered route rules and engagement state.
Commands, sticky replies and child addressing pass through the same worker
for immediate enqueue and polling. A route miss can feed onbod. Observe-only
routes record context without a guest. [ROUTING.md](ROUTING.md) owns precedence
and topic rules.

Turn control serializes a folder across chat JIDs and limits instance
concurrency. It saves input keys, ambient keys, cursor positions and attempt
identity in `turn_context`. Durable retry state includes `NextAttemptAt` and
status owed. `turn_control` holds the instance breaker and probe ownership.
Provider refusal, interruption and retry use these rows across restarts.
Ordinary retry delays are 30 seconds, 2 minutes and 5 minutes; scheduled input
uses 15, 45 and 180 minutes. Provider reset times can delay the next attempt.
A retry reruns the saved turn; it does not replay a deterministic workflow.

At dispatch, routd selects session, harness, packages, secrets and capability
decisions. routd decrypts selected secrets. runed receives model credentials
and explicitly granted shell keys; connectors resolve other credentials on
the host per tool call. The model gateway, when configured, supplies a key
for that turn and owns the spend ledger. routd revokes the key after the run.
`renderTurn` is runed's single renderer for mounts, environment, stdin and
egress rules. `Runtime` is exactly `Run(ctx, Turn)` and `Kill(handle)`.
QEMU-specific process, tap and CID state stays behind the handle.

runed claims a `spawns` row before execution. An occupied folder or full agent
capacity returns `Busy`. The QEMU runtime starts virtiofsd shares, a sealed
tap, an egress proxy and two vsock paths. Guest-init mounts a read-only rootfs
under a writable temporary overlay and projects the turn's mounts.
`RUNED_KERNEL_DIR` supplies `bzImage` and `initrd.gz`;
`RUNED_ROOTFS_DIR` supplies the exported agent filesystem and image environment.
Guest-init starts ant as uid `1000`. The guest's MCP socket connects through
socat and a CID-checked vsock bridge to routd's bound socket.
The guest home at `/home/node/` persists through the group's host directory.
Guest temporary filesystem changes end with the guest.

ant selects Claude, Codex or pi through the harness interface. Explicit tools
such as `reply` and `send` deliver content; final harness text is not a reply.
`submit_turn` reports outcome and session identity on the same socket.
routd rejects a closed attempt, records `turn_results`, updates session
continuity and finishes status. An addressed turn with no delivery can receive
one nudge. runed returns the execution outcome on the original HTTP call.

One progress renderer handles text, checklist, retry and final status.
`submit_status` updates one status row on channels that support it.
Progress edits have a five-second minimum interval. Completion clears typing
and persists cleanup debt before attempting delivery. The retry sweep repairs
owed status and pending outbound messages. Delivery uses a stable message ID
for adapter deduplication. Permanent refusals dead-letter immediately; transient
failures use exponential delays capped at one hour, with ten retries by default.
Social actions such as reactions record audit events rather than message rows.

### Store reads continue when no message arrives

| Entry point          | Cadence set by code                                 | Store work                                                                                                                                            |
| -------------------- | --------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Loop.Run` startup   | Once, before the ticker.                            | `recoverPending`, interrupted/deferred execution recovery, `recoverStatuses`, then `sweepChats` enqueues each chat with rows past its `agent_cursor`. |
| `Loop.Run` ticker    | `PollEvery`, default 2 seconds; reads no chat.      | Runs the entries below when they are due; writers wake their own chat.                                                                                |
| `maybeRecoverTurns`  | Every 30 seconds (`turnsEvery`), checked on a tick. | `q.recover` reads the running turns and the breaker state.                                                                                            |
| `flushProgress`      | Each tick, with a five-second publish limit.        | Reads turn/status rows for pending in-memory progress updates.                                                                                        |
| `maybeRetryOutbound` | Every 30 seconds, checked on a tick.                | `recoverStatuses` reads owed status; `PendingOutbound` selects up to 200 rows at least 30 seconds old, then applies each row's backoff.               |
| `ScheduleLoop`       | Once after recovery, then every minute.             | Claims due `scheduled_tasks`, calls ingest in-process, reschedules tasks and records `task_run_logs`.                                                 |
| `maybeGC`            | Every hour, checked on a tick.                      | Sweeps expired running turns and the retained message activity index.                                                                                 |

Timer work shares the poll goroutine except `ScheduleLoop`. A slow store read
delays the next maintenance check. Intervals express due checks, not deadlines.

Sources: [ingest](routd/server.go#L438-L560), [channel registration](chanlib/chanlib.go#L1),
[dispatch](routd/dispatch.go#L24), [durable deferral](routd/turn_control.go#L32),
[recovery and retry](routd/turn_control.go#L445-L755),
[poll and maintenance](routd/loop.go#L294-L562), [poll default](routd/loop.go#L173),
[scheduler](routd/schedule.go#L19-L76),
[run admission](runed/manager.go#L176-L224), [renderer](runed/render.go#L37-L86),
[runtime port](runed/runtime.go#L24-L60), [QEMU spawn](runed/qemu.go#L235),
[guest setup](kernel/guest-init/main.go#L376), [MCP bridge](runed/qemu.go#L769),
[harness](ant/src/backend.ts#L137), [outcome](ant/src/index.ts#L108-L122),
[submission](routd/turns.go#L162-L268), [status lifecycle](routd/turn_status.go#L138),
[status recovery](routd/turn_status.go#L618), [action audit](routd/social_audit.go#L1).

## Core vs integrations

`installed_packages` selects system packages and exact-folder packages.
`ant/arizuko.yml` declares the required `core` package. routd refreshes its
payload from the release at startup without a host build. Selection puts
core first, then other selected packages in installation order across both scopes.

```text
┌────────────────────────────────────────────────────────────┐
│ Package source: arizuko.yml, assets, optional compose      │
└──────────────────────────────┬─────────────────────────────┘
                               │ arizuko package add / sync
┌──────────────────────────────▼─────────────────────────────┐
│ installed_packages; <data_dir>/packages/<name>/            │
│ Owned rows; <data_dir>/services/<name>.yml                 │
└────────────────────────────────────────────────────────────┘
Turn selection ──► runed mounts selected payloads read-only.
arizuko generate ──► docker-compose.yml, scoped env and proxyd_routes.
```

Sources are `github.com/<owner>/<repo>[/<dir>][@<ref>]`, an absolute directory,
a `.tgz` absolute path or HTTP(S) URL, or a bundled service name.
Git sources resolve to commits; archives resolve to SHA-256 revisions.
Bundled services come from `template/services/<name>/` embedded in the binary.
Package identity comes from `name` in `arizuko.yml`.

The common installer maintains one payload at `<data_dir>/packages/<name>/`.
Explicit package commands can run the manifest's host install command.
Turns read selected payloads without running an installer. Docker owns the
compose fragment format; arizuko owns the manifest's typed resource rows.
The install record tracks owned row keys and service hashes. Updates replace
owned rows; removal withdraws them. An edited fragment refuses replacement.

`arizuko generate -i <instance>` reads `<data_dir>/services/*.yml` and
`<data_dir>/packages/<name>/arizuko.yml`,
writes scoped environment files, reconciles managed `proxyd_routes` rows,
and writes `<data_dir>/docker-compose.yml`. Backends use service names such
as `http://slakd:8080`. proxyd reads its routing table per request.
Package installation and Compose service startup are separate operations.
Authoring and commands: [EXTENDING.md](EXTENDING.md#installing-packages),
[compose/README.md](compose/README.md), [container/README.md](container/README.md).

Sources: [source resolver](cmd/arizuko/products.go#L65-L108),
[installed selection](routd/packages_store.go#L158), [core manifest](ant/arizuko.yml#L1),
[startup install](routd/cmd/routd/main.go#L61-L64),
[payload installer](container/packages.go#L103-L156),
[package ownership](cmd/arizuko/packages.go#L254),
[generation](cmd/arizuko/main.go#L183-L219), [fragment inclusion](compose/compose.go#L787-L875), [manifest path](compose/manifest.go#L82-L85).

## Auth separates identity from authority

Authd owns signing keys, signs ES256 access tokens and publishes JWKS at
`GET /v1/keys`. The operator CLI can also sign a folder-scoped user bearer by
reading authd's active key. Service clients exchange their
`AUTHD_SERVICE_KEY` for a `service:<daemon>` bearer and verify received tokens
against authd keys. Access tokens default to 15 minutes; refresh tokens rotate
within a family. Routd supplies grant scopes to authd over HTTP.

```text
┌────────────────────────────────────────────────────────────┐
│ Browser: login through authd; authenticated request        │
└──────────────────────────────┬─────────────────────────────┘
                               │ request through proxyd
┌──────────────────────────────▼─────────────────────────────┐
│ proxyd: verifies identity, strips client X-User-*          │
│ Stamps X-User-* and its service:proxyd bearer              │
└──────────────────────────────┬─────────────────────────────┘
                               │ forwarded request
┌──────────────────────────────▼─────────────────────────────┐
│ Backend: verifies transit proof, then authorizes target    │
└────────────────────────────────────────────────────────────┘
authd publishes JWKS for offline verification at each receiver.
```

Proxyd is the sole `X-User-*` stamper. It removes client identity headers,
verifies identity, reads current scope grants, and replaces Authorization with
its `service:proxyd` bearer. Backends trust the headers only after that transit
proof. Route-token chat and webhook requests can be anonymous at proxyd;
webd validates the token against hashed `route_tokens` rows.
Davd relies on proxyd's file and folder checks. The speech API relies on its
internal network placement. These are explicit service-specific boundaries.

Routd owns `acl` and `acl_membership`. An ACL row names principal, action,
scope, params, predicate, effect and grant option. Matching local rows decide
before defaults; deny wins within that layer. No matching allow denies.
`AuthorizeTurn` gives the routed folder its granted authority. The attested
sender adds authority only inside the folder's subtree. Denies apply on both
axes. A path's depth grants nothing. Elevated `/root` turns bypass ordinary
tool gates; ACL mutations still require `CheckGrant`.

Sources: [token lifetime](authd/main.go#L20-L23), [operator signing](cmd/arizuko/token.go#L160-L187),
[key endpoint](authd/http.go#L103-L109), [grants federation](authd/grants.go#L33),
[transit proof](auth/middleware.go#L7-L38), [stamping](proxyd/main.go#L924-L977),
[token surface](webd/route_token.go#L1), [WebDAV gate](proxyd/main.go#L655),
[speech API](ttsd/main.go#L45-L52), [row model](core/types.go#L278),
[evaluator](auth/authorize.go#L61-L147), [turn axes](routd/sibling_db.go#L245-L252),
[elevation](routd/agent_gate.go#L26-L45), [ACL mutation](routd/acl_resource.go#L149-L160).
Operator reference: [GRANTS.md](GRANTS.md), [auth/README.md](auth/README.md).

## Cold tier vs hot tier

Cold-tier resources share a hand-written handler between REST and MCP.
`secrets` exposes write-only REST; `network_rules` exposes MCP only.
`resreg` supplies schema, binding, replacement and OpenAPI emission; the caller
supplies caller construction and authorization. `Authz` resolves target scope
and params before `Gate` checks access. Resource names identify REST
`/v1/<name>` paths and default MCP prefixes. `MCPNames` can override action
names. Names must stay globally unique.

Operator resources use `auth.Authorize` by default or a surface-specific gate.
Agent mounts bind the per-turn `AuthorizeTurn` function. Some agent resources
check target containment inside the handler and set `agentAllowGate` to skip
the operator default. ACL mutations use `CheckGrant` and prove the checked
policy version in their write.
Hot-tier chat actions such as `reply`, `send` and `inspect_*` are MCP tools.
`submit_turn`, `submit_status` and `check_turn` are hidden protocol methods,
not model-visible tools. Each HTTP daemon's mounted resource set determines
its OpenAPI document; resource registration alone does not advertise a route.
The deployment also includes non-resreg services such as davd and ttsd.

Sources: [resource contract](resreg/resreg.go#L201-L232), [REST binding](resreg/resreg.go#L344),
[MCP binding](routd/mcp.go#L728), [secret endpoints](resreg/resources/secrets.go#L21),
[network endpoints](resreg/resources/network_rules.go#L19),
[agent containment seam](routd/agent_gate.go#L71-L80), [hidden methods](ipc/ipc.go#L518-L529).
Handler design: [resreg/README.md](resreg/README.md).

## Daemon conventions

Cross-daemon APIs carry wire types through `api/v1/`. A daemon owns its
handler and schema preparation. Shared IDs in `types/` prevent API contracts
from depending on daemon internals. Shippable components accept mechanism
inputs rather than importing arizuko domain packages.

Ownership does not mean exclusive access. `StoreReach` declares direct foreign
openers. Proxyd reads `proxyd_routes`, `web_routes` and ACL policy on the
request path, so current routing and revocations apply on the next request.
Webd resolves route tokens itself. Dashd and onbod perform operator/admission
writes directly. Gitd, googd and slakd have the direct reads named in the map.
Runed consumes the resolved turn and opens only `runed.*`.

Sources: [dependency tests](tests/components/imports_test.go#L1),
[reach contract test](store/owner_reach_test.go#L1),
[proxyd snapshots](proxyd/main.go#L130-L155), [scope read](proxyd/main.go#L965),
[web token lookup](webd/main.go#L65), [execution store](runed/db.go#L1).

## Decided, and why

| Boundary                                             | Reason and code                                                                                                                                                                                                                |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Two storage operations; shared protocols above them. | A caller must remain independent of its storage engine. Pagination and atomic checks belong to the driver. [port contract](objstore/objstore.go#L1-L16).                                                                       |
| One turn renderer; two execution operations.         | Policy decisions reach the guest in one envelope. The QEMU implementation delivers that envelope and keeps its process state behind a handle. [renderer](runed/render.go#L37-L40), [execution port](runed/runtime.go#L24-L60). |
| Persistent group home; fresh guest for execution.    | Session files and work survive turns. The temporary root overlay does not retain changes between guests. [home mount](runed/mounts.go#L56-L59), [overlay](kernel/guest-init/main.go#L416-L426).                                |
| Egress rules outside the guest.                      | The host seals the tap to the proxy, so guest code cannot bypass a domain decision with direct network traffic. [tap rules](runed/vmnet.go#L80-L101).                                                                          |
| Current ACL reads on proxyd's request path.          | Revocation takes effect while an existing access token still verifies. [revocation test](proxyd/hotpath_read_test.go#L20-L64).                                                                                                 |

## The instance keeps files while the server keeps rows

The default instance directory is `/srv/data/arizuko_<instance>/`.
`ARIZUKO_DATA_DIR` or `PREFIX` can select its base. Mounted daemon views use
`/srv/app/home`; compose passes identity through environment values.

| Path                                                         | Owner and lifetime                                                                                              |
| ------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------- |
| `<data_dir>/.env`                                            | Operator infrastructure inputs and generated compose identity values; never a daemon's shared env file.         |
| `<data_dir>/env/<daemon>.env`                                | Generated scoped inputs, service identity and that daemon's enrolled store pair; generation preserves the pair. |
| `<data_dir>/env/operator.env`                                | Host CLI store credential; overlays `.env`, with matching endpoint and CA required.                             |
| `<data_dir>/services/<name>.yml`                             | Installed compose fragment consumed by generation.                                                              |
| `<data_dir>/packages/<name>/`                                | Current package payload and `arizuko.yml` manifest; selections and ownership live in `installed_packages`.      |
| `<data_dir>/groups/<folder>/`                                | Persistent guest home, harness session files, diary, logs and attachments.                                      |
| `<data_dir>/ipc/<folder>/gated.sock`                         | Routd's per-turn socket; stale sockets are removed at startup.                                                  |
| `<data_dir>/web/pub/`, `<data_dir>/web/priv/`                | Published and authenticated files served by proxyd.                                                             |
| `<data_dir>/store/teled/`                                    | Telegram update offset.                                                                                         |
| `<data_dir>/store/whatsapp-auth/`, `<data_dir>/store/whapd/` | WhatsApp pairing state and persistent inbound/outbound ledgers.                                                 |
| `<data_dir>/vms/`                                            | QEMU records and staging mounts; `RUNED_VM_DIR` can set another location.                                       |
| `RUNED_KERNEL_DIR`, `RUNED_ROOTFS_DIR`                       | Read-only release artifacts; can be shared by instances at their configured absolute paths.                     |

```text
┌────────────────────────────────────────────────────────────┐
│ Instance .env, service fragments and package manifests     │
└──────────────────────────────┬─────────────────────────────┘
                               │ arizuko generate
┌──────────────────────────────▼─────────────────────────────┐
│ docker-compose.yml, scoped env and managed route rows      │
└──────────────────────────────┬─────────────────────────────┘
                               │ systemctl restart arizuko_<instance>
┌──────────────────────────────▼─────────────────────────────┐
│ Daemons read image assets; groups and store persist        │
└────────────────────────────────────────────────────────────┘
```

`arizuko:latest` supplies the Go daemons and release assets at
`/opt/arizuko/ant/`. `arizuko-ant:latest` supplies the guest rootfs.
`make rootfs` exports its filesystem and environment metadata into a
content-keyed directory. `make kernel` builds the kernel and guest-init initrd.
Runed checks the configured artifacts and host devices before serving turns.
WhatsApp, davd and ttsd use their separate images from the process map.

`arizuko run -i <instance>` generates configuration and runs Compose for initial
setup. A managed deployment applies a release with
`sudo systemctl restart arizuko_<instance>`. Keep the external store running
independently. `APP_SRC_DEV=1` binds source into daemon containers for development;
a release reads its image assets. Build, bootstrap, restart and backup steps
belong in [INSTALL.md](INSTALL.md).

Sources: [instance path](cmd/arizuko/main.go#L236-L250),
[host env loading](core/config.go#L168-L220), [scoped env](compose/compose.go#L357-L442),
[guest artifact mounts](compose/compose.go#L1121-L1205), [socket path](groupfolder/folder.go#L1),
[WhatsApp files](whapd/src/main.ts#L45-L65), [images](Dockerfile#L1),
[rootfs build](Makefile#L311-L363), [kernel build](Makefile#L29-L79),
[artifact checks](runed/qemuhost.go#L117), [initial run](cmd/arizuko/main.go#L150),
[restart procedure](INSTALL.md#systemd-keeps-the-instance-running).

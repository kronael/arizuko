---
status: shipped
shipped: 2026-06-14
depends: [1-auth-standalone, 17-openapi-mcp, K-ant-backend-codex]
---

# runed — the execution plane

**Decided.** `runed` is the **execution plane** — the daemon that runs
the agent container per turn. It owns the **container execution
envelope**: per-folder serialization, the per-spawn container lifecycle
(spawn/steer/runTTL/teardown), and the run record in `runed.db`. It holds
`docker.sock` and the crackbox egress attach; nothing else in the
platform spawns a container.

`runed` does **not** host the agent's MCP socket. `routd` hosts the
per-turn agent MCP socket **in-process** (`routd/mcp.go` `ServeTurnMCP`)
and serves every agent tool from its own process; `runed` mounts the ipc
dir into the container so the in-container agent reaches that socket.
`runed` never appends a message and never signs. Companion spec
[`E-routd.md`](E-routd.md); the two are written to **one** PINNED
`POST /v1/runs` + `/v1/turns/{turn_id}/*` contract, including the `busy`
retryable-reject discriminator. `routd` decides _whether/where_ a batch
runs, renders the prompt, hosts the socket; `runed` _runs_ the container.

## Boundaries — owns / never

| Concern                                     | runed                                                                                    |
| ------------------------------------------- | ---------------------------------------------------------------------------------------- |
| Per-folder serialization                    | **owns** (read from `spawns`, § Run state)                                               |
| Per-spawn container lifecycle               | **owns** (`container/`, driven by `runed/docker.go`)                                     |
| Per-spawn runtime state + run history       | **owns** (`runed.db`: `spawns`, `spawn_logs`, `session_log`, `circuit_breaker`)          |
| Per-turn agent MCP unix socket + tool host  | **never** — `routd` (`ServeTurnMCP`, in-process); runed mounts the ipc dir               |
| Session-id LINEAGE (`sessions`, topic fork) | **never** — `routd` (runed produces the id, routd persists it)                           |
| Capability tokens for spawned agents        | **never** — removed 2026-08-01 (§ Capability brokering); the turn is socket-credentialed |
| Routing decisions / rules / events          | **never** — `routd`                                                                      |
| Conversation messages (append/history)      | **never** — `routd`, via `/v1/turns/*`                                                   |
| Group / route IDENTITY (`groups`, `routes`) | **never** — `routd`                                                                      |
| Token **signing**                           | **never** — `authd` (sole signer)                                                        |

`runed` holds no copy of group↔folder identity — it receives `folder` on
each `POST /v1/runs` and resolves the workspace path mechanically
(`GROUPS_DIR/<folder>`).

"Owns" here is a **write-discipline convention, not an enforced
boundary**: compose mounts the whole instance data dir rw as UID 1000
into every core daemon (`compose/compose.go`), so any daemon could open
`runed.db`. The rule is that none does.

## Run state — `runed.db` is the only source of truth

`runed` keeps **no in-memory runtime state**. Every admission decision
reads the DB, none is cached: exclusivity (`GetActiveSpawn`), the
concurrency cap (`ActiveCount` vs `MAX_CONCURRENT_CONTAINERS`, default
5), and the circuit breaker (`GetFailures` over the `circuit_breaker`
table, 3 consecutive failures opens it). Admission is one atomic claim
under `BEGIN IMMEDIATE` — insert a `running` row or reject
(`runed/manager.go`, schema `runed/migrations/`).

**WHY DB-stateless:** an in-memory admission map lies across a restart —
a crashed container leaves a folder wedged with no row to reconcile, and
the concurrency cap silently resets. Boot reconciliation
(`DB.ExpireOrphans`) marks `running` rows whose containers are gone as
`exited`/`error` — not `killed`, which is reserved for an explicit kill —
so the atomic claim is correct from the first request after boot.

**WHY reject instead of queue (queue-drop 2026-07-13):** an internal FIFO
`waiting` queue duplicated routd's DB-backed dispatch queue, and two
queues drift. `runed` is now a **pure claim-or-reject executor** — when
the folder is busy or the cap is hit it returns `busy=true` on the pinned
response and `routd` re-feeds the batch. `routd` does not advance the
`agent_cursor` and does not count the reject toward the circuit breaker
(busy ≠ error); the retry is bounded by routd's poll interval. Exception:
a busy folder with a _live_ container takes the new batch as a **steer**,
not a reject — addressed deterministically from the `spawns` row
(container name + `IPC_DIR/<folder>/`), never from a stored closure.

**Session-id ownership.** The `sessions` table — per-`(folder, topic)`
`session_id` plus topic lineage — lives in **`routd.db`, not `runed.db`**
([`E-routd.md`](E-routd.md) § Topic lineage). `runed` _produces_ the id
(the harness emits it), `routd` _persists_ it; `runed` reads the resume
id off the request, never from its own DB. The `spawns.session_id` column
is a runtime **echo** only.

## The execution-session envelope

One agent turn is **one owned sequence** with a single owner of the
lifetime — atomic claim → container spawn → stream/collect → teardown —
bounded by the same deadline timers, with teardown on every exit path
(`defer`). The sequence is `runed/manager.go` + `runed/docker.go`; the
prose here would only restate it.

Two properties that are decisions, not mechanism:

- **Readiness is spawn success.** arizuko has no `/readyz`; "started"
  means the container read stdin. Don't add a health probe to the spawn
  path.
- **Three timers, none crossing a daemon boundary.** Hard deadline
  `Stop`s the container; the soft deadline (hard − 2 min) injects a "wrap
  up NOW" IPC message and `SIGUSR1`s it; the idle timeout resets on each
  `[ant]` stderr line, capped at 240 resets (≈4 h).

Frames arrive **out of band**: the agent's tool calls hit routd's
in-process MCP socket during the run, and the agent reports its turn via
`submit_turn` (REST twin `POST /v1/turns/{turn_id}/result`) — `runed`
never sees the conversation.

## The container model — `container/`

- **Per-turn-ephemeral.** `docker run -i --rm`, one container per turn,
  no warm pool. Pluggable behind the `Runtime` interface
  (`runed/runtimes.go`); `dockerRuntime` (`runed/docker.go:64`) is
  production, `FakeRuntime` backs the contract + unit tests. There is no
  `LocalRuntime`.
- **FHS mounts** (`container/runner.go` `buildMounts`): group workspace →
  `$HOME` (`/home/node`); `/opt/arizuko` (RO app src); `/run/ipc` (the
  socket dir routd serves into); the web slots `/var/lib/www` (RO public
  tree), `~/public_html` → `/pub/<folder>/`, `~/private_html` →
  `/priv/<folder>/` ([`V-web-vhosts.md`](V-web-vhosts.md)); `/var/lib/share`
  (grant-gated world share); `/var/lib/groups` (root only); layered
  `.codex` creds. Mount paths are configured-not-derived (CLAUDE.md
  § Identity is configured).
- **Egress isolation.** Register the container IP with crackbox, attach
  `--network <egress-net>`, set `HTTP(S)_PROXY`.
- **Backend is opaque to the envelope.** Claude Code today, `codex
app-server` second ([`K-ant-backend-codex.md`](K-ant-backend-codex.md)).
  `runed` writes JSON to stdin and drains stdout/stderr — it must stay
  harness-agnostic.
- **Graceful shutdown — containers outlive the accept loop.** On
  SIGTERM/SIGINT runed stops accepting `POST /v1/runs`, **detaches**
  (does not kill) live containers so the agent can still finish against
  routd's socket, waits `RUNED_SHUTDOWN_GRACE`, then exits. Killing
  in-flight turns to exit faster is the wrong trade — the deadline
  injection already wraps them up.

## Sub-agent spawning (via routd, not a runed endpoint)

There is **no** `POST /v1/runs/{run_id}/spawn`. Sub-agent delegation
flows through `routd`: `routd/spawn.go` `spawnFromPrototype` materializes
a child group under `parentFolder/<sanitized>`, and `routd/steer.go`
`delegateViaMessage(depth+1)` issues a normal `POST /v1/runs` for the
child. From runed's view the child is just another run. Depth is capped
at 1 in routd.

## Auth

`runed` **offline-verifies** every caller's token (routd, operator,
agent) via `auth.VerifyHTTP` against authd's cached JWKs — no signing key
in `runed`, `iss` pinned to `"authd"`, scope + folder checked per
endpoint. It holds a `service:runed` token (`auth.ServiceToken`,
`AUTHD_SERVICE_KEY`) for its own daemon→daemon calls.

## Capability brokering — REMOVED (2026-08-01)

runed no longer brokers a per-spawn capability token, and `mcp_tokens` is
dropped (`runed/migrations/0003-drop-mcp-tokens.sql`). The mechanism was
built, wired to authd, and never consumed:

- No token reached the agent. `container.Input` had no token field and
  `runed/docker.go` dropped `RunSpec.Token`; the spec's delivery claim was
  amended away 2026-07-11 and the code was never followed up.
- The token could not have named a principal anyway. `runed/broker.go`
  hardcoded `typ:"downscoped"` and authd's `Downscope` sets
  `Sub: parent.Sub` (`authd/tokens_test.go`), so every token would have
  read `sub=service:runed` — one identity for every tenant.
- Nothing verified a `jti`: zero references in `ipc/` or `routd/mcp.go`.

**What credentials a turn instead**: the per-folder unix socket, gated by
`SO_PEERCRED` (`ipc/ipc.go:542` `peerCred`). That is an unforgeable
capability reference — it cannot be copied out of the container and it
dies with the turn. A bearer string handed to an agent that runs
attacker-influenced text with a shell, a persistent `$HOME`, and egress
can be written to disk and replayed after the turn ends; the socket
cannot.

Consequences: a spawn no longer makes a synchronous authd call, so authd
being unreachable no longer aborts a spawn. Revocation is unaffected and
stronger than a token TTL would allow — `auth.Authorize` reads the ACL
live, so a revoked grant stops the next tool call inside a live turn
(`routd/revocation_live_test.go`).

If a turn credential is ever wanted again, `5/32 § Caching` states the
contract it must preserve: the token names the principal and never
carries the permissions.

> Stale code comments still list `mcp_tokens`
> (`runed/cmd/runed/main.go:111`, `runed/db_test.go:8`,
> `cmd/arizuko/migrate_split.go:169`). The migration dropped the table;
> the comments are wrong.

## Standalone-ready acceptance

One contract test ([`contract_test.go`](../../runed/contract_test.go)):
boots with a temp `runed.db`, `RUNTIME=fake`, an `IPC_DIR`, and a stub
`ROUTD_URL` accepting `/v1/turns/*`; accepts a stub `POST /v1/runs`,
spawns a `FakeRuntime` "container" that connects + submits a turn, and
returns `{run_id, outcome:"ok", session_id}`.

**Core carve-out (honest bar).** `runed` is not a strict no-`core` build:
`docker.go` imports `core` (`core.GroupConfig`, `core.Config`) to drive
the runner. The published wire contract (`runed/api/v1/`) depends only on
`types/`; the runtime glue still reads `core`.

## Code pointers

- `runed/manager.go` — DB-stateless admission + lifecycle. The only
  in-memory state is the container-lifetime steer callbacks.
- `runed/docker.go` — `dockerRuntime`: the per-turn Docker runner.
- `runed/runtimes.go` — `FakeRuntime` + the `Runtime` seam.
- `runed/db.go` — `runed.db` access (`CreateSpawn`, `RecordSession`,
  `EndSession`, `ExpireOrphans`, `SweepExpired`).
- `runed/migrations/` — the schema (0001 initial, 0002 circuit breaker,
  0003 drop mcp_tokens).
- `routd/mcp.go` — `ServeTurnMCP`, the agent socket routd hosts.
- [`1-auth-standalone.md`](1-auth-standalone.md) — `auth.ServiceToken`,
  `auth.VerifyHTTP`.
- [`E-routd.md`](E-routd.md) — the PINNED wire contract (routd side).

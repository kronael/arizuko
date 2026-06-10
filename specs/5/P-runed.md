---
status: partial
depends:
  [
    1-auth-standalone,
    5-uniform-mcp-rest,
    3-user-spawned-agents,
    K-ant-backend-codex,
  ]
---

# runed — the execution plane

> **Status (2026-06-10): partial.** The shipped surface is done: `runed`
> is the sole container-spawner (`docker.sock` + crackbox egress attach),
> owns the per-folder serialization / circuit-breaker / runTTL / steer +
> the per-spawn capability-token broker, and records every run in
> `runed.db`. The remaining gap is the **DB-stateless executor refactor**
> (this spec's run-state design): `manager.go` still holds in-memory
> maps; it must read `spawns` per admission (§ Run state).

**Decided.** `runed` is the **execution plane** — the daemon that runs the
agent container per turn. It owns the **container execution envelope**:
per-folder serialization, the per-spawn container lifecycle
(spawn/steer/runTTL/teardown), and the brokering of downscoped capability
tokens for the agents it spawns. It holds `docker.sock` and the crackbox
egress attach; nothing else in the platform spawns a container.

`runed` does **not** host the agent's MCP socket. `routd` hosts the
per-turn agent MCP socket **in-process** (`ServeTurnMCP`) and serves every
agent tool from its own process; `runed` mounts the ipc dir into the
container so the in-container agent reaches that socket. `runed` never
appends a message and never signs. The companion spec is
[`E-routd.md`](E-routd.md); the two are written to **one** PINNED
`POST /v1/runs` + `/v1/turns/{turn_id}/*` contract. `routd` decides
_whether/where_ a batch runs, renders the prompt, hosts the socket;
`runed` _runs_ the container.

`status: partial` = the DB-stateless refactor below is design-led and not
yet built, not design-open. The wire-level `POST /v1/runs` +
`/v1/turns/*` contract is PINNED identical with
[`E-routd.md`](E-routd.md) § The routd↔runed interface.

## Boundaries — owns / brokers / never

| Concern                                     | runed                                                                      |
| ------------------------------------------- | -------------------------------------------------------------------------- |
| Per-folder serialization                    | **owns** (read from `spawns`, § Run state)                                 |
| Per-spawn container lifecycle               | **owns** (`container/`)                                                    |
| Per-turn agent MCP unix socket + tool host  | **never** — `routd` (`ServeTurnMCP`, in-process); runed mounts the ipc dir |
| Per-spawn runtime state + run history       | **owns** (`runed.db`: `spawns`, `session_log`)                             |
| Session-id LINEAGE (`sessions`, topic fork) | **never** — `routd` (runed produces the id, routd persists it)             |
| Capability tokens for spawned agents        | **brokers** (downscope via `authd`)                                        |
| Routing decisions / rules / events          | **never** — `routd`                                                        |
| Conversation messages (append/history)      | **never** — `routd`, via `/v1/turns/*`                                     |
| Group / route IDENTITY (`groups`, `routes`) | **never** — `routd`                                                        |
| Token **signing**                           | **never** — `authd` (sole signer)                                          |

`runed` holds no copy of group↔folder identity — it receives `folder` on
each `POST /v1/runs` and resolves the on-disk workspace path mechanically
(`GROUPS_DIR/<folder>`).

## Run state — `runed.db` is the only source of truth

**Design (spec leads; code conforms).** `runed` keeps **no in-memory
runtime state**. The `spawns` table IS the run-state source of truth; every
admission decision reads it, none is cached:

- **Exclusivity** ("is this folder busy?"):
  `SELECT 1 FROM spawns WHERE folder=? AND state='running'`.
- **Concurrency cap**:
  `SELECT count(*) FROM spawns WHERE state='running'` vs
  `MAX_CONCURRENT_CONTAINERS`.
- **Circuit breaker**: the consecutive-failure count is a persisted column
  (or a small per-folder table) read per admission, not a `map[folder]int`.

**Admission is one atomic DB claim.** On `POST /v1/runs`, runed runs a
single transaction:

```
BEGIN IMMEDIATE
  if no running row for folder AND running-count < cap:
       INSERT spawns(state='running', ...)   -- claim the slot
       COMMIT  → run it
  else:
       ROLLBACK → return busy to routd
```

If the folder is busy or the cap is hit, runed returns **busy** to `routd`,
which already re-feeds the batch on its own queue. `runed` keeps **no
internal admission queue** — that duplicated routd's queue. `runed` is a
**pure claim-or-reject executor**; `routd` owns all queueing. (Exception:
the steer-into-running path below — a busy folder with a live container
takes the new batch as a steer, not a reject.)

**Steer addresses the live container deterministically.** No stored
closure: from the `spawns` row for the busy folder, runed derives the
container name and the IPC socket path from `folder`/`run_id`
(`container_name` column + `IPC_DIR/<folder>/`), writes the steer batch,
and signals it.

**Boot reconciliation.** On startup runed scans `spawns` for
`state='running'` rows whose containers are gone (crashed mid-run before a
clean teardown) and marks them `killed`. After reconciliation the `running`
set reflects only live containers, so the atomic claim is correct from the
first request.

> **Implementation gap (open).** The current `manager.go` still holds
> in-memory `active` / `failures` / `activeCount` / `waiting` maps. The
> refactor to this DB-stateless design (atomic spawns-table admission,
> deterministic steer, boot reconciliation, drop the internal queue) is
> tracked in bugs.md.

## runed.db schema

`runed` owns `runed.db` — its own SQLite file (WAL), its own
`runed/migrations/` subdir. Times are RFC3339 TEXT; all token-ref columns
store an opaque ref or hash, never a raw token.

**Session-id ownership.** The `sessions` table — per-`(folder, topic)`
`session_id` plus topic-lineage columns — lives in **`routd.db`, not
`runed.db`** ([`E-routd.md`](E-routd.md) § Topic lineage). `session_id` is
opaque to routd: `runed` _produces_ it (the harness emits it) and returns
it on the `POST /v1/runs` backstop + `submit_turn`; `routd` _persists_ it.
`runed` reads the **resume** `session_id` off the `POST /v1/runs` request,
never from its own DB. The `spawns.session_id` column runed keeps is a
runtime **echo** (the value this spawn ran/resumed, resolved at envelope
step 3) — distinct from routd's lineage-authoritative `sessions`; runed
reads it only to echo on run-status / steer-ack.

```sql
-- session_log: one row per agent invocation (audit / dashd run history).
-- runed is the writer (RecordSession at spawn start, EndSession at exit).
CREATE TABLE session_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  group_folder  TEXT NOT NULL,
  session_id    TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  ended_at      TEXT,
  result        TEXT,                   -- "success" | "error" | "silent"
  error         TEXT,
  message_count INTEGER
);
CREATE INDEX idx_session_log_folder ON session_log(group_folder, id DESC);

-- spawns: one row per container spawn (the execution envelope) AND the
-- run-state source of truth (§ Run state). state drives admission.
-- run_id is the public handle returned by POST /v1/runs and used by
-- GET/DELETE /v1/runs/{run_id}.
CREATE TABLE spawns (
  run_id         TEXT PRIMARY KEY,      -- "run_<rand>"; the public handle
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL,         -- arizuko-<instance>-<safe-folder>-<unixmilli>
  session_log_id INTEGER REFERENCES session_log(id),
  mcp_token_jti  TEXT,                  -- the brokered token for this spawn (mcp_tokens.jti)
  session_id     TEXT,                  -- runtime ECHO of the harness session id this spawn ran/resumed;
                                        --   resolved at step 3 (resume value or freshly minted UUID).
                                        --   NOT lineage-authoritative — routd.sessions owns lineage.
  state          TEXT NOT NULL,         -- queued|running|ended|killed
                                        --   ended subsumes natural exit / timeout / error (see outcome).
  outcome        TEXT,                  -- ok|error|silent (set at end; NULL while running)
  exit_code      INTEGER,
  steered        INTEGER NOT NULL DEFAULT 0, -- 1 if any steer-into-running write happened
  failures       INTEGER NOT NULL DEFAULT 0, -- consecutive-failure count for the breaker (§ Run state)
  created_at     TEXT NOT NULL,
  started_at     TEXT,                  -- container start
  ended_at       TEXT
);
CREATE INDEX idx_spawns_folder ON spawns(folder, created_at DESC);
CREATE INDEX idx_spawns_state ON spawns(state);

-- mcp_tokens: the downscoped tokens runed BROKERS from authd per spawn.
-- runed does not sign — it persists the REF to correlate audit, enforce
-- one-token-per-live-spawn, and revoke-by-expiry sweep. jti is authd's
-- token id; parent_jti is the runed service token it was downscoped from.
-- Distinct token FAMILY from routd's route-tokens (5/W): those gate
-- inbound web routes; these gate an agent's outbound /v1/* calls.
CREATE TABLE mcp_tokens (
  jti        TEXT PRIMARY KEY,          -- authd-assigned token id
  run_id     TEXT NOT NULL UNIQUE REFERENCES spawns(run_id) ON DELETE CASCADE,
                                        -- UNIQUE: one brokered token per spawn (§ brokering)
  parent_jti TEXT NOT NULL,             -- runed's own service token jti (the downscope parent)
  folder     TEXT NOT NULL,             -- arz/folder claim the token is scoped to
  scope      TEXT NOT NULL,             -- JSON array of granted scope strings
  issued_at  TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX idx_mcp_tokens_expiry ON mcp_tokens(expires_at);
```

An hourly GC goroutine deletes `spawns` rows (cascading `mcp_tokens`) older
than `RUNED_SPAWN_RETENTION` (default 7 days) and any `mcp_tokens` past
`expires_at`. `runed` never stores a raw token; the agent receives the JWS
once at spawn (§ envelope step 2) and `runed` keeps only the `jti`.

## The execution-session envelope (the critical section)

One agent turn is **one owned sequence** — slot claim, token brokering,
container spawn, output streaming, teardown — with a single owner of the
lifetime. `runFor(run_id)` is the sequence. Every step is bounded by the
same deadline timers; teardown runs on every exit path (`defer`):

```
POST /v1/runs {folder, topic, turn_id, message_batch (rendered prompt), capability_scopes, …}
  └─ atomic claim (§ Run state):  if folder busy → steer; if cap hit → return busy; else INSERT running
       └─ runFor(run_id):         # one slot, one goroutine
            1. broker token:  POST authd /v1/tokens  (downscope mode)
                 Authorization: Bearer <runed service token>
                 { sub forced to caller, scope ⊆ runed scope ∩ capability_scopes,
                   folder: <folder>, typ:"downscoped", ttl_seconds: <= run deadline }
                 → persist {jti, run_id, parent_jti, folder, scope, expires_at}
                   into mcp_tokens; hold the JWS in memory for step 2.
            2. spawn container (docker run -i --rm):
                 - resolve session_id: if POST /v1/runs.session_id != "" use it
                   (resume); else generate a fresh UUIDv4 NOW (runed mints the
                   harness session id — opaque to routd). Written to stdin,
                   session_log, AND spawns.session_id (the runtime echo).
                 - mounts (§ container), egress register, --network <egress-net>,
                   HTTP(S)_PROXY when isolated
                 - mount IPC_DIR/<folder>/ so the agent reaches routd's socket
                 - write JSON Input (prompt, sessionId, folder, the brokered
                   token, operator anchors) to stdin, close stdin
                 - RecordSession(folder, session_id, now) → session_log row
                 - spawns row already state=running (claimed at admission); set started_at=now
            3. detect readiness:
                 - "started" == container reads stdin; arizuko has NO /readyz.
                   Spawn returning successfully IS ready.
            4. stream / collect (frames arrive OUT-OF-BAND during the run):
                 - drain stderr line-scanner; "[ant]" lines reset the idle timer (cap 240 resets)
                 - the agent's tool calls hit routd's in-process MCP socket; routd appends
                   messages + fans out (the agent's REST twin is /v1/turns/{turn_id}/*).
                 - the agent reports its turn via submit_turn (JSON-RPC, hidden from
                   tools/list) → handled in-process by routd (REST twin
                   POST /v1/turns/{turn_id}/result)
            5. teardown (defer; runs on natural exit, timeout, kill):
                 - cmd.Wait() → exit code; stop idle/deadline/soft timers
                 - unregisterEgress
                 - EndSession(rowID, harness_newSessionId, result, err, msgs):
                   if the harness emitted a newSessionId, update the session_log
                   row's session_id via COALESCE(NULLIF(?,''), session_id)
                 - spawns.state=ended (outcome ok|error|silent) or killed; ended_at;
                   on failure: spawns.failures++ for the folder (breaker, § Run state)
                 - write host log file; the slot frees implicitly (no running row)
```

**Deadlines (carried from `container/runner.go`).** Hard deadline `Stop`s;
the soft deadline (hard − 2 min) injects a "wrap up NOW" IPC message and
`SIGUSR1`s the container. **Idle timeout** resets on each `[ant]` line,
capped at 240 resets (≈4 h). All run inside the one envelope; none crosses
a daemon boundary.

## Capability-token brokering

`runed` **mints nothing.** Per spawn it calls `authd`'s downscope endpoint
to obtain a scoped token for the agent
([`1-auth-standalone.md`](1-auth-standalone.md) § `POST /v1/tokens`
downscope mode, `auth.MintNarrower`):

```
POST authd /v1/tokens   Authorization: Bearer <runed service token>
{ "typ":"downscoped", "sub":"<caller>", "scope": <runed.scope ∩ capability_scopes>,
  "folder":"<folder>", "ttl_seconds": <remaining run deadline> }
→ 200 { "token":"<jws>", "jti":"...", "expires_at":"..." }
```

- `runed` holds a `service:runed` token, exchanged at boot via
  `auth.ServiceToken` (`AUTHD_SERVICE_KEY`). Its declared `service_scope`
  (`template/services/runed.toml`) is the **ceiling** for any agent token
  it brokers — the downscope guarantees scope ⊆ parent. `service_scope`
  **MUST include `tokens:mint`**: brokering an agent token under a
  user/service `sub` is an **issuer-mint** (the caller is not the subject),
  legal only for an issuer-mint-authorized service per
  [`1-auth-standalone.md`](1-auth-standalone.md) § issuer-mint. Without
  `tokens:mint` in runed's scope, `authd` rejects the downscope and no
  agent can be spawned.
- The requested `scope` is `runed`'s own scope ∩ the `capability_scopes`
  `routd` passed. `authd` enforces scope ⊆ parent and folder ⊆
  parent-folder, returning `403 scope_exceeds_parent` on violation —
  `runed` cannot escalate an agent beyond its own authority.
- Distinct from routd's route-tokens (5/W): those authenticate _inbound_
  web/webhook traffic; `mcp_tokens` authenticate the agent's _outbound_
  `/v1/*` calls. Different family, table, owner.
- **Token delivery is PINNED: the MCP socket handshake, not an env var.**
  When the in-container agent's MCP client connects (to routd's socket),
  the JWS is returned in the `initialize` response `_meta.capability_token`
  field. It is **not** a docker `-e` env var — env vars leak into
  `docker inspect`, `/proc/1/environ`, and shelled-out sub-processes. The
  socket is already `SO_PEERCRED`-gated, so the handshake is the tighter
  carrier. `runed` persists only the `jti`; the raw JWS lives in the
  agent's process memory.
- An agent's `mint_token` MCP call (sub-agent delegation) forwards to
  `authd` with the **agent's own token** as parent — `runed` does not
  re-broker; `authd` downscopes from the agent's token directly. `runed`
  only brokers the _initial_ per-spawn token.

## The queue + container model

Carried from `queue/queue.go` and `container/runner.go`. Per-folder
serialization, the concurrency cap, and the circuit breaker are the
admission decisions of § Run state (read from `spawns`, not cached); this
section is the container-side mechanics.

### Serialization + breaker (read from `spawns`)

- **Per-folder serialization.** One live spawn per folder; a second
  `POST /v1/runs` for a busy folder steers (§ envelope) or returns busy.
  Two JIDs in the same folder never run concurrently (they share a
  workspace; concurrent writes corrupt session state).
- **Concurrency cap.** `MAX_CONCURRENT_CONTAINERS` (default 5,
  `core.Config.MaxContainers`); over the cap → return busy (routd re-feeds).
- **Circuit breaker.** 3 consecutive failures for a folder opens the
  breaker; a new inbound resets it. The run that trips it returns
  `outcome:"error"`, `error:"circuit breaker open"`, `breaker_open:true`
  on the pinned response. `routd` reads `breaker_open` and surfaces "send
  another message to retry". No separate breaker-report endpoint — the
  signal rides the one response the caller awaits.
- **Steer-into-running-container.** Writes IPC files + `SIGUSR1`
  (§ envelope; the live container is addressed deterministically from its
  `spawns` row).
- **Graceful shutdown — containers outlive the accept loop.** On
  SIGTERM/SIGINT `runed` (1) stops accepting new `POST /v1/runs`, (2)
  detaches (does NOT kill) the running containers — the agent can still
  call tools + `submit_turn` against routd's socket — and waits up to
  `RUNED_SHUTDOWN_GRACE` (default = the longest live run's remaining
  deadline) for them to exit `--rm`, then (3) exits. A spawn outliving the
  grace window is left detached; the deadline injection already wrapped the
  turn.

### Container (the executor) — `container/`

- **Per-turn-ephemeral.** `docker run -i --rm` — one container per turn,
  no warm pool (`buildArgs`). Pluggable behind the `Runtime` interface;
  `dockerRuntime` is the production runtime, `FakeRuntime` backs the
  contract + unit tests.
- **Idle / deadline / soft-deadline timers** (§ envelope step 5).
- **FHS mounts** (from `buildMounts`): group workspace → `$HOME`
  (`/home/node`); `/opt/arizuko` (RO app src); `/run/ipc` (the MCP socket
  dir routd serves into); per-tier web slots — `/var/lib/www` (RO public
  tree, tier ≤ 2), `~/public_html` (→ `/pub/<folder>/`), `~/private_html`
  (→ `/priv/<folder>/`) ([`V-web-vhosts.md`](V-web-vhosts.md));
  `/var/lib/share` (world share, grant-gated); `/var/lib/groups` (root
  only); layered `.codex` creds (K-backend). Mount paths are
  configured-not-derived (CLAUDE.md § Identity is configured).
- **Egress isolation.** Register the container's IP with the egress
  backend (crackbox), attach `--network <egress-net>`, set `HTTP(S)_PROXY`.
  Tier ≤ 1 (operator bots) get unconstrained egress (append `*`).
- **Backend choice.** Claude Code today; Codex `app-server` is the second
  harness behind the `Backend` interface
  ([`K-ant-backend-codex.md`](K-ant-backend-codex.md)). The harness is an
  implementation detail of the spawned container; `runed`'s envelope is
  harness-agnostic (writes JSON to stdin, drains stdout/stderr).

## Sub-agent spawning (via routd, not a runed endpoint)

There is **no** `POST /v1/runs/{run_id}/spawn` endpoint. Sub-agent
delegation flows through `routd`: `routd/spawn.go` `spawnFromPrototype`
materializes a child group under `parentFolder/<sanitized>`, and
`routd/steer.go` `delegateViaMessage(depth+1)` issues a normal
`POST /v1/runs` for the child folder. `runed` simply records each run in
`spawns` (`db.go` `CreateSpawn`) like any other run — from runed's view the
child is just another `POST /v1/runs`. Depth is capped at 1 in routd; the
child token is brokered like the parent's (§ brokering), narrowed to the
parent agent's scope.

## Auth

- **runed offline-verifies** every caller's token (routd, operator, agent)
  via the `auth/` library against `authd`'s cached JWKs (`auth.VerifyHTTP`).
  No signing key in `runed`. `iss` pinned to `"authd"`; scope + folder
  checked per endpoint.
- **runed holds a `service:runed` token** (`auth.ServiceToken`,
  `AUTHD_SERVICE_KEY`) for its own daemon→daemon calls (the downscope
  request to `authd`) and as the **parent** of every brokered agent token.
- **runed brokers, never signs** agent capability tokens (§ brokering).

## Standalone-ready acceptance

One contract test in [`contract_test.go`](../../runed/contract_test.go):

> Boots with a temp `runed.db`, `RUNTIME=fake`, an `IPC_DIR`, a stub
> `AUTHD_URL` returning a fixed downscoped token, and a stub `ROUTD_URL`
> accepting `/v1/turns/*`. Accepts a stub
> `POST /v1/runs {folder:"demo", message_batch:"...", capability_scopes:[...]}`,
> brokers a (faked) capability token, spawns a `FakeRuntime` "container"
> that connects + submits a turn, and returns
> `{run_id, outcome:"ok", session_id}`.

`FakeRuntime` (next to the `Runtime` interface, `runed/runtimes.go`) backs
the contract + unit tests of the queue + envelope without spawning a real
container. There is no `LocalRuntime` — only `dockerRuntime` (production)
and `FakeRuntime` (tests).

**Core carve-out (honest bar).** `runed`'s binary is not a strict
no-`core` build: `docker.go` imports `core` (`core.GroupConfig`,
`core.Config`) to drive the runner. The published wire contract
(`runed/api/v1/`, [`39-runed-interface.md`](39-runed-interface.md))
depends only on `types/`; the runtime glue still reads `core`.

## Touches

| Component            | Change                                                                                                                                                                        |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `runed/`             | queue + container + run-state in `runed.db` + `migrations/`; serves `/v1/runs`, `/v1/sessions`.                                                                               |
| `runed/api/v1/`      | wire types + thin client for `POST /v1/runs` etc.                                                                                                                             |
| `routd` (sibling)    | hosts the agent MCP socket in-process; serves `/v1/turns/{turn_id}/*`; calls `runed POST /v1/runs`; owns `groups`/`routes`/messages; sub-agent spawn (`spawn.go`/`steer.go`). |
| `authd`              | downscope endpoint brokers each agent token (5/1).                                                                                                                            |
| `compose/compose.go` | seed `service:runed` key + `service_scope`; `[[proxyd_route]]` for `/v1/runs`.                                                                                                |

## Code pointers

- `runed/manager.go` — the admission + lifecycle manager. **Currently
  in-memory** (`active`/`failures`/`activeCount`/`waiting`); the DB-stateless
  refactor (§ Run state) is open (bugs.md).
- `runed/docker.go` — `dockerRuntime`: the per-turn Docker runner (mounts,
  egress, idle/soft/hard deadlines, steer); imports `core`.
- `runed/runtimes.go` — `FakeRuntime` + the `Runtime` seam.
- `runed/db.go` — `runed.db` read/write (`CreateSpawn`, `RecordSession`,
  `EndSession`, `SweepExpired`).
- `routd/mcp.go` — `ServeTurnMCP` (routd hosts the agent socket in-process).
- [`1-auth-standalone.md`](1-auth-standalone.md) — `authd` downscope,
  `auth.ServiceToken`, `auth.VerifyHTTP`.
- [`E-routd.md`](E-routd.md) — the PINNED `POST /v1/runs` + `/v1/turns/*`
  wire contract (routd side).

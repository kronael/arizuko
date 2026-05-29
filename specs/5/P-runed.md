---
status: partial
depends:
  [
    U-genericization,
    1-auth-standalone,
    5-uniform-mcp-rest,
    3-user-spawned-agents,
    K-ant-backend-codex,
  ]
---

# runed — the execution plane

**Decided.** `runed` is the **execution plane** carved out of `gated`
([`U-genericization.md`](U-genericization.md) Phase C). It owns the full
**execution-session envelope** end to end: the work queue, the per-tenant
MCP socket, the per-spawn container lifecycle, and the brokering of
downscoped capability tokens for the agents it spawns.

The former `mcpd` is **folded in** — no separate MCP-host daemon. The
unix socket, the capability token, the container spawn, and the teardown
are **one execution session owned wholly by `runed`** (§ The
execution-session envelope for why this is structural). The
[`U-genericization.md`](U-genericization.md) Phase C table listing
`routd`/`runed`/`mcpd` as three daemons is superseded on the `mcpd` row:
`mcpd`'s charter (per-tenant MCP socket, capability-token brokering, tool
federation) is `runed`'s.

`status: draft` = code not yet built (the gated split is a later release
than `authd`), not design-open. The companion spec is
[`E-routd.md`](E-routd.md); the two are written to **one** PINNED
`POST /v1/runs` + `/v1/turns/{turn_id}/*` contract (§ The routd↔runed
interface). `routd` decides _whether/where_ a batch runs and renders the
prompt; `runed` _runs_ it, federating the agent's tool calls back to
routd out-of-band — `runed` never appends a message and never signs.

## Boundaries — owns / brokers / never

| Concern                                     | runed                                                          |
| ------------------------------------------- | -------------------------------------------------------------- |
| Work queue (per-folder serialization)       | **owns** (`queue/` carried in)                                 |
| Per-spawn container lifecycle               | **owns** (`container/` carried in)                             |
| Per-tenant MCP unix socket + tool host      | **owns** (`ipc/` folded in, was `mcpd`)                        |
| Per-spawn runtime state + run history       | **owns** (`runed.db`: `spawns`, `session_log`)                 |
| Session-id LINEAGE (`sessions`, topic fork) | **never** — `routd` (runed produces the id, routd persists it) |
| Capability tokens for spawned agents        | **brokers** (downscope via `authd`)                            |
| Routing decisions / rules / events          | **never** — `routd`                                            |
| Conversation messages (append/history)      | **never** — `routd`, via `/v1/turns/*`                         |
| Group / route IDENTITY (`groups`, `routes`) | **never** — `routd`                                            |
| Token **signing**                           | **never** — `authd` (sole signer)                              |

`runed` holds no copy of group↔folder identity — it receives `folder` on
each `POST /v1/runs` and resolves the on-disk workspace path mechanically
(`GROUPS_DIR/<folder>`). The only runtime "group prep" distinct from
routing identity is the live container-slot bookkeeping the queue keeps
**in memory** (active folder, container name); not persisted, no table.

## runed.db schema

`runed` owns `runed.db` — its own SQLite file (WAL), its own
`runed/migrations/` subdir, per the
[`U-genericization.md`](U-genericization.md) DB-ownership rule. Times are
RFC3339 TEXT; all token-ref columns store an opaque ref or hash, never a
raw token.

**Session-state ownership (reconciled with
[`E-routd.md`](E-routd.md)).** The `sessions` table — per-`(folder,
topic)` `session_id` plus the topic-lineage columns — lives in
**`routd.db`, not `runed.db`** ([`E-routd.md`](E-routd.md) § Topic
lineage + sessions). The lineage is a routing/turn-lifecycle concern, and
`session_id` is **opaque to routd**: `runed` _produces_ it (the harness
emits it) and returns it on the `POST /v1/runs` backstop + `submit_turn`;
`routd` _persists_ it. One owner of session lineage, no drift. `runed`
reads the **resume** `session_id` off the `POST /v1/runs` request, never
from its own DB. The `spawns.session_id` column runed does keep is a
runtime **echo** (the value this spawn ran/resumed, resolved at envelope
step 4) — distinct from routd's lineage-authoritative `sessions`; runed
reads it only to echo on run-status / steer-ack, never to decide resume.

`runed.db` therefore holds only **execution runtime state** with no home
in routd. Cutover (one-shot, big-bang, NO BACKWARD COMPATIBILITY): the
`session_log` source is gated's `messages.db` (`store/migrations/0001`),
copied here then dropped; gated's `sessions` migrates to **`routd.db`**;
`spawns`/`spawn_logs`/`mcp_tokens` are new (no source rows).

```sql
-- session_log: one row per agent invocation (audit / dashd run history).
-- Carried verbatim from store/migrations/0001. runed is now the writer
-- (RecordSession at spawn start, EndSession at exit).
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

-- spawns: one row per container spawn (the execution-session envelope).
-- run_id is the public handle returned by POST /v1/runs and used by
-- GET/DELETE /v1/runs/{run_id} + the output stream.
CREATE TABLE spawns (
  run_id         TEXT PRIMARY KEY,      -- "run_<rand>"; the public handle
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL,         -- arizuko-<instance>-<safe-folder>-<unixmilli>
  session_log_id INTEGER REFERENCES session_log(id),
  mcp_token_jti  TEXT,                  -- the brokered token for this spawn (mcp_tokens.jti)
  session_id     TEXT,                  -- runtime ECHO of the harness session id this spawn ran/resumed;
                                        --   resolved at step 4 (resume value or freshly minted UUID).
                                        --   NOT lineage-authoritative — routd.sessions owns lineage (opaque to routd);
                                        --   this is runed's local copy so run-status + steer-ack can echo it.
  state          TEXT NOT NULL,         -- queued|running|exited|timeout|error|killed
  outcome        TEXT,                  -- ok|error|silent (set at exit; NULL while running)
  exit_code      INTEGER,
  steered        INTEGER NOT NULL DEFAULT 0, -- 1 if any steer-into-running write happened
  created_at     TEXT NOT NULL,
  started_at     TEXT,                  -- container start
  ended_at       TEXT
);
CREATE INDEX idx_spawns_folder ON spawns(folder, created_at DESC);
CREATE INDEX idx_spawns_state ON spawns(state);

-- spawn_logs: append-only per-spawn event/output log. The [ant]-prefixed
-- stderr lines + lifecycle events, so GET /v1/runs/{run_id}/output can
-- replay them and dashd can show run detail without the host log file.
CREATE TABLE spawn_logs (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES spawns(run_id) ON DELETE CASCADE,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL,                -- "agent" | "lifecycle" | "stderr"
  line    TEXT NOT NULL
);
CREATE INDEX idx_spawn_logs_run ON spawn_logs(run_id, id);

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

An hourly GC goroutine deletes `spawns` rows (cascading `spawn_logs` +
`mcp_tokens`) older than `RUNED_SPAWN_RETENTION` (default 7 days) and any
`mcp_tokens` past `expires_at`. `runed` never stores a raw token; the
agent receives the JWS once at spawn (§ envelope step 3) and `runed`
keeps only the `jti`.

## The execution-session envelope (the critical section)

This is the load-bearing reason `mcpd` is folded into `runed`. One agent
turn is **one owned sequence** — socket creation, token brokering, MCP
host start, container spawn, output streaming, teardown — with a single
owner of the lifetime. Splitting the socket/token half into a separate
daemon would force a distributed lifetime (two processes racing on the
same socket path, `run_id`, teardown) — exactly the failure mode the
queue's `stopOnce`/`SendMessages` race guards already fight inside one
process. Keep it one process, one owner.

`runFor(run_id)` is the sequence. Every step is bounded by the same
deadline timers; teardown runs on every exit path (`defer`):

```
POST /v1/runs {folder, topic, turn_id, message_batch (rendered prompt), capability_scopes, …}
  └─ runed.Enqueue(folder)        # queue serializes per-folder (§ queue)
       └─ runForGroup(folder):    # one slot, one goroutine
            1. socket path:  IPC_DIR/<folder>/gated.sock
                 - os.Remove stale, net.Listen("unix", path), chmod 0660,
                   chown expectedUID (1000 = ant `node`, or host uid in dev)
            2. broker token:  POST authd /v1/tokens  (downscope mode)
                 Authorization: Bearer <runed service token>
                 { sub forced to caller, scope ⊆ runed scope ∩ capability_scopes,
                   folder: <folder>, typ:"downscoped", ttl_seconds: <= run deadline }
                 → persist {jti, run_id, parent_jti, folder, scope, expires_at}
                   into mcp_tokens; hold the JWS in memory for step 4.
            3. start MCP host on the socket (ServeMCP):
                 - SO_PEERCRED gate (peer uid == expectedUID)
                 - register the federated tool surface (§ MCP host + federation)
                 - bound accept fan-out (maxMCPConns = 8)
            4. spawn container (docker run -i --rm):
                 - resolve session_id: if POST /v1/runs.session_id != "" use it
                   (resume); else generate a fresh UUIDv4 NOW (runed mints the
                   harness session id — opaque to routd). Written to stdin,
                   session_log, AND spawns.session_id (the runtime echo
                   run-status/steer-ack read); the harness resumes it or reports
                   newSessionId at exit (step 7).
                 - mounts (§ container), egress register, --network <egress-net>,
                   HTTP(S)_PROXY when isolated
                 - write JSON Input (prompt, sessionId, folder, the brokered
                   token, operator anchors) to stdin, close stdin
                 - RecordSession(folder, session_id, now) → session_log row
                 - spawns row state=running, started_at=now
            5. detect readiness:
                 - "started" == container reads stdin; arizuko has NO /readyz.
                   Spawn returning successfully IS ready (U-gen ContainerRuntime
                   § "WaitForReady doesn't apply").
            6. stream / collect (frames arrive OUT-OF-BAND during the run):
                 - drain stderr line-scanner; "[ant]" lines reset the idle timer
                   (cap 240 resets) AND append a spawn_logs(kind=agent) row
                 - the agent's tool calls hit the MCP host (step 3); message
                   tools forward to routd /v1/turns/{turn_id}/* (§ federation)
                 - the agent reports its turn via submit_turn (JSON-RPC, hidden
                   from tools/list) → forwarded to routd
                   POST /v1/turns/{turn_id}/result (§ submit_turn)
            7. teardown (defer; runs on natural exit, timeout, kill):
                 - cmd.Wait() → exit code; stop idle/deadline/soft timers
                 - stopMCP()  (cancel accept loop, ln.Close, os.Remove sock)
                 - unregisterEgress
                 - EndSession(rowID, harness_newSessionId, result, err, msgs):
                   if the harness emitted a newSessionId, update the session_log
                   row's session_id via COALESCE(NULLIF(?,''), session_id)
                 - spawns.state=exited|timeout|killed, spawns.outcome, ended_at;
                   write host log file
                 - free the queue slot, drain waiters
```

**Deadlines (carried verbatim from `container/runner.go`).** Hard
deadline `Stop`s; the soft deadline (hard − 2 min) injects a "wrap up NOW"
IPC message and `SIGUSR1`s the container. **Idle timeout** resets on each
`[ant]` line, capped at 240 resets (≈4 h). All run inside the one
envelope; none crosses a daemon boundary.

## MCP host + tool federation

`runed` hosts the per-tenant MCP socket (the folded-in `mcpd`) and serves
the in-container agent its tool surface as a **thin API gateway** — local
in-process calls for runed-owned operations, HTTP forwards for everything
else ([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) § MCP federation):

| Tool family                                                                                                                                           | Hosted as   | Backend                                                          |
| ----------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ---------------------------------------------------------------- |
| `reply`, `send`, `send_file`, `send_voice`, `post`, `like`, `dislike`, `forward`, `quote`, `repost`, `edit`, `delete`, `pin_message`, `unpin_message` | **forward** | `routd` — message append + platform fan-out (sole message owner) |
| `get_history`, `get_thread`, `fetch_history`, `inspect_messages`, `find_messages`                                                                     | **forward** | `routd` (message store)                                          |
| `spawn` (sub-agent), `kill`, `delegate_group`, `escalate_group`                                                                                       | **local**   | `runed` — enqueue a child/parent run (§ spawn-a-sub-agent)       |
| `inspect_session`, run status                                                                                                                         | **local**   | `runed.db` (`spawns`, `session_log`)                             |
| `set_routes`, `add_route`, `delete_route`, `register_group`, `inspect_routing`                                                                        | **forward** | `routd` (routes / group identity)                                |
| `list_tasks`, `pause_task`, scheduled-task ops                                                                                                        | **forward** | `timed` `/v1/tasks`                                              |
| `whoami`, `mint_token`, `verify_token`                                                                                                                | **forward** | `authd` (`auth.MCPTools`)                                        |
| MCP connectors (`<connector>_<tool>`)                                                                                                                 | **local**   | runed-spawned stdio subprocess (`ipc/connector.go`)              |

The federation forward (carried from `ipc/ipc.go`'s `GatedFns`, repointed
at HTTP). Most message tools map tool-name → `/v1/turns/{id}/<name>`
directly (`reply`→`/reply`, `send`→`/send`, `get_history`→`/history`,
`get_thread`→`/thread`, `like`→`/like`, `edit`→`/edit`,
`delete`→`/delete`). Four tools do **not** — the tool name is not the path
tail, and runed must special-case them on forward (PINNED, identical in
[`E-routd.md`](E-routd.md) § verb→path exceptions):

- `send_file` → `/document`.
- `dislike` → `/like` with `reaction="👎"`. There is **no** `/dislike`
  endpoint (the dislike-via-like-emoji rule: one code path per mechanism,
  both verbs visible to the agent).
- `pin_message` → `/pin`, `unpin_message` → `/unpin` (strip `_message`;
  the bare verb tail is routd's interface — sending `/pin_message` 404s).
- `unpin_all` → `/unpin` with `all:true` (the `/v1/turns/{id}/unpin` body
  carries the `all` flag; the `_all` tool name has no separate path).

```
agent → ipc.tools/call(reply, {chatJid, text, replyToId})
      → runed validates the agent token scope (messages:send:own_group)
      → runed HTTP-POST routd /v1/turns/{turn_id}/reply
             { jid, text, reply_to_id }   X-Idempotency-Key: <per-call>
             Authorization: Bearer <agent capability token>   # the brokered token
      → routd verifies token (offline JWKs), checks scope, appends + fans out,
             returns { message_id, platform_id, status }
      → runed returns the JSON-RPC result to the agent
```

`runed` knows the `turn_id` (it owns the socket↔spawn binding, and
`turn_id` arrived on `POST /v1/runs`), so it stamps `{turn_id}` into the
forward path; the agent never sees it. The agent's last-reply/engagement
bookkeeping that `recordOutbound` does in-process today becomes routd's
job — routd owns the message row, so it owns `SetLastReply`/
`BumpEngagement`. This is the **one-renderer-many-sinks** rule (CLAUDE.md):
routd renders every message side effect; `runed` is a sink-router.

Scope checks use the `:own_group` suffix form: `messages:send:own_group`
for sends/reactions, `chats:read:own_group` for history.

`tools/list` is filtered to the agent's granted set
([`11/17-mcp-firewall.md`](../11/17-mcp-firewall.md)): `runed` derives the
flat allowed-tool list from the brokered token's `scope` and gates the
socket — a denied tool is invisible, not just un-callable. Until
[`11/18-openapi-mcp.md`](../11/18-openapi-mcp.md) lands (REST→MCP via
`x-mcp-*`), the forward table is hand-rolled (carried from `ipc/ipc.go`).

## Capability-token brokering

`runed` **mints nothing.** Per spawn it calls `authd`'s downscope
endpoint to obtain a scoped token for the agent
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
  `tokens:mint` in runed's scope, `authd` rejects the downscope as an
  unauthorized issuer-mint and no agent can be spawned.
- The requested `scope` is `runed`'s own scope ∩ the `capability_scopes`
  `routd` passed. `authd` enforces scope ⊆ parent and folder ⊆
  parent-folder, returning `403 scope_exceeds_parent` on violation —
  `runed` cannot escalate an agent beyond its own authority.
- Distinct from routd's route-tokens (5/W): those authenticate _inbound_
  web/webhook traffic; `mcp_tokens` authenticate the agent's _outbound_
  `/v1/*` calls. Different family, table, owner.
- **Token delivery is PINNED: the MCP socket handshake, not an env var.**
  When the in-container agent's MCP client connects (step 3), `runed`
  returns the JWS in the `initialize` response `_meta.capability_token`
  field. It is **not** a docker `-e` env var — env vars leak into
  `docker inspect`, `/proc/1/environ`, and shelled-out sub-processes. The
  socket is already `SO_PEERCRED`-gated, so the handshake is the tighter
  carrier. `runed` persists only the `jti`; the raw JWS lives in the
  agent's process memory.
- An agent's `mint_token` MCP call (sub-agent delegation) forwards to
  `authd` with the **agent's own token** as parent — `runed` does not
  re-broker; `authd` downscopes from the agent's token directly. `runed`
  only brokers the _initial_ per-spawn token.

## The routd↔runed interface (PINNED)

`routd` owns routing + messages; `runed` owns execution. This spec and
[`E-routd.md`](E-routd.md) § The routd↔runed interface are written to
match **exactly**.

### `POST /v1/runs` — run (or steer) an agent turn

Called by `routd` after it decided a batch routes to `folder` and rendered
the prompt. Auth: a `routd` service token (Bearer). `message_batch` is the
**rendered prompt STRING** (`sysMsgs+autocalls+persona+<observed>+feed`),
not an array — routd renders, runed runs.

```jsonc
// POST <RUNED_URL>/v1/runs   Authorization: Bearer <routd service token>
{
  "folder": "acme/eng",
  "topic": "deploy",                    // "" = main; thread_ts / forum topic otherwise
  "chat_jid": "slack:T/C/U",
  "session_id": "uuid-or-empty",        // empty = fresh; runed resumes if non-empty
  "message_batch": "<rendered prompt>", // STRING, not an array
  "trigger_sender": "u_abc",            // engagement-bump skip ONLY (5/G); NOT token identity
  "caller_sub": "user:u_abc",           // token SUBJECT for the brokered agent token (§ brokering):
                                        //   inbound user's canonical sub when a user triggered the run;
                                        //   "service:routd" for daemon-triggered runs; never ""
  "turn_id": "wamid.X",                 // triggering inbound id; echoed on the callbacks
  "capability_scopes": ["messages:send:own_group", "chats:read:own_group", "..."],
  "model": "",                          // group override; empty = instance default
  "container_config": { /* opaque GroupConfig from groups.container_config */ },
  "isolated": false                     // timed-isolated:* runs: one-off container, no session persist
}
// 200 (sync, run complete)
{ "run_id":"run-…", "outcome":"ok"|"error"|"silent", "session_id":"uuid", "error":"",
  "steered": false,        // discriminator: false = turn-boundary outcome; true = steer ack (§ steer)
  "breaker_open": false }  // true ONLY on the run that trips the circuit breaker (§ queue)
// 503 {"error":"queue_shutting_down"}
```

**Sync, frames out-of-band.** The call is **synchronous for the turn
boundary**: `routd` blocks on the HTTP response, which returns when the
run completes (mirrors today's `runner.Run` return). The agent's
conversation frames arrive **out-of-band during the run** via the
`/v1/turns/{turn_id}/*` callbacks below, **not** in this response body.
`submit_turn` is the canonical end-of-turn signal; the response carries
`outcome` + `session_id` as a **backstop** if the agent never called
`submit_turn` (e.g. crash). routd reconciles: `submit_turn`'s `session_id`
/`error` wins over the response body when both arrive.
`GET /v1/runs/{run_id}/output` (§ surface) is a separate operator/dashd
observation surface, not the routd path.

**Outcome semantics** (the contract `routd` keys on): `ok` — ran, may or
may not have replied (advance cursor, record); `error` — run failed
(container exited non-zero, timed out, or agent reported `status:error`;
advance past batch, mark rows errored, send failure notice; carries a
short tail); `silent` — ran, no deliverable output (logged, no error;
distinct from `ok` so routd doesn't expect a delivered message).

**Steer-into-running-container — the one async path.** If `folder`
already has a live spawn when `POST /v1/runs` arrives, `runed` does
**not** start a second container — it writes the new `message_batch` as an
IPC input file into the running container's `IPC_DIR/<folder>/in/` and
`SIGUSR1`s it (carried from `queue.SendMessages`). This call **returns
immediately** (does not block on the steered turn): `200 {run_id:<existing>,
outcome:"ok", session_id:<existing>, steered:true}` — an **ack**, not a
turn-boundary outcome. The steered batch's frames join the
already-running run; that run's terminal outcome is reported on the
_original_ (still-open) `POST /v1/runs` response. `routd` keys on
`steered:true` to know this is an ack (do not advance the cursor; the
original run's response governs the batch). If the signal fails (runner
already exited — the documented race in `queue/queue.go`), `runed` falls
through to a fresh **synchronous** spawn (the IPC file is drained by the
next container at session start) and the response is a normal
turn-boundary outcome with `steered:false`.

### The agent's callback into routd: `/v1/turns/{turn_id}/*`

The agent's message tools (hosted by `runed`, § federation) call back into
`routd` — `runed` **never writes a message**. `routd` serves (PINNED,
[`E-routd.md`](E-routd.md) § Turn / conversation commands;
`X-Idempotency-Key` required on every call):

| Method + path                       | Body                                       | Result                              |
| ----------------------------------- | ------------------------------------------ | ----------------------------------- |
| `POST /v1/turns/{turn_id}/reply`    | `{jid, text, reply_to_id?}`                | `{message_id, platform_id, status}` |
| `POST /v1/turns/{turn_id}/send`     | `{jid, text}`                              | `{message_id, platform_id, status}` |
| `POST /v1/turns/{turn_id}/document` | `{jid, path, name, caption, reply_to_id?}` | `{message_id, status}`              |
| `GET  /v1/turns/{turn_id}/history`  | `?jid&before&limit&q`                      | `{source, messages:[...], cap}`     |
| `GET  /v1/turns/{turn_id}/thread`   | `?reply_to&limit`                          | `{messages:[...]}`                  |
| `POST /v1/turns/{turn_id}/like`     | `{jid, platform_id, reaction}`             | `{ok:true}`                         |
| `POST /v1/turns/{turn_id}/edit`     | `{jid, platform_id, content}`              | `{ok:true}`                         |
| `POST /v1/turns/{turn_id}/delete`   | `{jid, platform_id}`                       | `{ok:true}`                         |
| `POST /v1/turns/{turn_id}/pin`      | `{jid, platform_id}`                       | `{ok:true}`                         |
| `POST /v1/turns/{turn_id}/unpin`    | `{jid, platform_id, all}`                  | `{ok:true}`                         |
| `POST /v1/turns/{turn_id}/result`   | `TurnResult` (`submit_turn` REST twin)     | `{recorded: true\|false}`           |

`{turn_id}` is passed on `POST /v1/runs` and echoed by `runed` onto every
callback (`routd` knows the folder/topic context, so the agent never
re-states it). Auth on every callback: the **agent's brokered capability
token** (Bearer), verified offline by `routd` against `authd`'s JWKs,
scope-checked (`messages:send:own_group` for sends/reactions,
`chats:read:own_group` for history). `runed` injects `{turn_id}` and
forwards the token verbatim; it does **not** re-sign or re-scope.
Unsupported platform verb → `422 unsupported` (maps
`chanlib.ErrUnsupported`), relayed to the agent with the platform hint.

### `submit_turn` → `POST /v1/turns/{turn_id}/result`

The agent's per-turn `submit_turn` JSON-RPC method (hidden from
`tools/list`, `ipc/ipc.go`) is handled by `runed`'s MCP host, which (a)
records `session_id` + run outcome into `runed.db` (`session_log` via
`EndSession`, `spawns.outcome`/`spawns.session_id`) — **not cost**, and
(b) forwards the `TurnResult` (including the per-model **cost** breakdown)
to `routd /v1/turns/{turn_id}/result` so `routd` (the message + turn
owner) records delivery + `cost_log` + `round_done` SSE.
**Cost ownership: `routd` persists `cost_log`; `runed` only reports cost
in the payload** ([`E-routd.md`](E-routd.md) § cost_log). Idempotency is
enforced by `routd` on `(folder, turn_id)`; a duplicate returns
`{recorded:false}`.

## The queue + container model

Carried verbatim from `queue/queue.go` and `container/runner.go`; `runed`
is the new owner. Nothing in the mechanics changes — only the daemon
boundary.

### Queue (the scheduler) — `queue/`

- **Per-folder serialization.** One live spawn per folder; a second
  `POST /v1/runs` for a busy folder either steers (§ above) or queues.
- **Folder-exclusivity.** `activeFolders[folder] = jid` ensures no two
  JIDs in the same folder run concurrently (they share a workspace;
  concurrent writes corrupt session state).
- **Concurrency cap.** `MAX_CONCURRENT_CONTAINERS` (default 5,
  `core.Config.MaxContainers`); over the cap → `waitingGroups`, drained on
  each completion (`drainWaitingLocked`).
- **Circuit breaker.** 3 consecutive failures opens the breaker for a
  folder; a new inbound resets it. The run that trips it returns
  `outcome:"error"`, `error:"circuit breaker open"`, `breaker_open:true`
  (the explicit field on the pinned response). `routd` reads
  `breaker_open` and surfaces "send another message to retry". No separate
  breaker-report endpoint — the signal rides the one response the caller
  awaits.
- **Steer-into-running-container.** `SendMessages` writes IPC files +
  `SIGUSR1` (§ `POST /v1/runs` steer).
- **Graceful shutdown — the socket outlives the accept loop.** On
  SIGTERM/SIGINT `runed` (1) stops accepting new `POST /v1/runs`, (2) keeps
  every **in-flight spawn's MCP host + socket alive** so the container can
  still call tools + `submit_turn`, (3) detaches (does NOT kill) the
  containers and waits up to `RUNED_SHUTDOWN_GRACE` (default = the longest
  live run's remaining deadline) for them to exit `--rm`, tearing each
  socket down per-spawn as its container exits (§ envelope step 7), then
  (4) exits. Shutdown never drains a _shared_ socket mid-run — each
  per-spawn socket lives exactly as long as its own container. A spawn
  outliving the grace window is left detached; its socket is gone, so late
  tool calls fail closed (the deadline injection already wrapped the turn).

### Container (the executor) — `container/`

- **Per-turn-ephemeral.** `docker run -i --rm` — one container per turn,
  no warm pool (`buildArgs`). Pluggable behind the `ContainerRuntime`
  interface ([`U-genericization.md`](U-genericization.md) § ContainerRuntime;
  `DockerRuntime` default, `LocalRuntime` for CI/standalone).
- **Idle / deadline / soft-deadline timers** (§ envelope step 7).
- **FHS mounts** (carried from `buildMounts`): group workspace → `$HOME`
  (`/home/node`); `/opt/arizuko` (RO app src); `/run/ipc` (the MCP socket
  dir); per-tier web slots — `/var/lib/www` (RO public tree, tier ≤ 2),
  `~/public_html` (→ `/pub/<folder>/`), `~/private_html` (→
  `/priv/<folder>/`) ([`V-web-vhosts.md`](V-web-vhosts.md));
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

## The rest of the `/v1/*` surface

All `/v1/*` JSON errors use `{"error":"<code>","message":"<human>"}` with
the HTTP status carrying the class. Every `/v1/*` endpoint except
`GET /openapi.json` (public, before auth) requires a bearer token verified
offline against `authd`'s JWKs (§ Auth).

### `GET /v1/runs/{run_id}` — run status

```jsonc
// GET /v1/runs/run_8f3a   Authorization: Bearer <routd|operator>
// 200
{
  "run_id": "run_8f3a",
  "folder": "atlas/main",
  "topic": "",
  "state": "running",
  "outcome": null,
  "session_id": "sess_...",
  "steered": false,
  "created_at": "...",
  "started_at": "...",
  "ended_at": null,
}
// 404 {"error":"unknown_run"}
```

`session_id` here is read straight from `spawns.session_id` (the runtime
echo set at envelope step 4) — the same source the steer-ack response
(`steered:true`) returns. runed never consults routd's `sessions` for
this; lineage stays routd's.

### `GET /v1/runs/{run_id}/output` — streamed / collected output

`Accept: text/event-stream` → SSE replay of `spawn_logs` rows + live tail
until the spawn exits, then a terminal `event: outcome` frame.
`Accept: application/json` → a snapshot `{lines:[...], outcome}`.

### `DELETE /v1/runs/{run_id}` — kill / stop

Bearer scope `runs:kill`. Gracefully stops the container
(`StopContainerArgs`, then `docker kill`, then `rm -f` — carried from
`container/runner.go`). Idempotent: killing an already-exited run is a
no-op `200`. Sets `spawns.state=killed`, frees the queue slot.

```jsonc
// DELETE /v1/runs/run_8f3a   Authorization: Bearer <operator>
// 200 { "killed": true }   |   404 {"error":"unknown_run"}
```

### `GET /v1/sessions` — session lifecycle (read)

Lists `session_log` rows for a folder (dashd run history). Bearer scope
`sessions:read`; folder bounded by the token's `arz/folder`.

```jsonc
// GET /v1/sessions?folder=atlas/main&limit=20
// 200 { "sessions":[ {id, session_id, started_at, ended_at, result, message_count}, ... ] }
```

### `POST /v1/runs/{run_id}/spawn` — spawn a sub-agent

The local `spawn`/`delegate`/`escalate` MCP tools resolve here. Enqueues a
**child** run (or a parent escalation) on `runed`'s own queue — sub-agent
spawning never leaves the execution plane. Depth capped at 1 (carried from
`ipc/ipc.go` delegation depth). The sub-agent's token is brokered like the
parent's (§ brokering) with `scope` narrowed to the parent agent's scope
(the agent's `mint_token` is the delegation path; `runed` enforces
⊆-parent at enqueue).

```jsonc
// POST /v1/runs/run_8f3a/spawn   Authorization: Bearer <agent token>
{
  "relation": "child|parent",
  "target_folder": "atlas/main/sre",
  "prompt": "...",
  "chat_jid": "slack:ws/channel/C01",
}
// 202 { "run_id":"run_9c2b", "queued": true }
// 403 {"error":"depth_exceeded"|"scope_exceeds_parent"}
```

## Auth

- **runed offline-verifies** every caller's token (routd, operator, agent)
  via the `auth/` library against `authd`'s cached JWKs (`auth.VerifyHTTP`).
  No signing key in `runed`. `iss` pinned to `"authd"`; scope + folder
  checked per endpoint.
- **runed holds a `service:runed` token** (`auth.ServiceToken`,
  `AUTHD_SERVICE_KEY`) for its own daemon→daemon calls (the downscope
  request to `authd`) and as the **parent** of every brokered agent token.
- **runed brokers, never signs** agent capability tokens (§ brokering).
- **The MCP socket** keeps the kernel-attested `SO_PEERCRED` uid gate
  (carried from `ipc/ipc.go`): only a peer whose uid matches the
  container's expected uid may speak MCP. The brokered token is the
  over-the-wire boundary for forwards.

## Standalone-ready acceptance

The [`U-genericization.md`](U-genericization.md) § Acceptance bar for
`runed`. One contract test in `tests/standalone/runed_test.go`:

> Boots with `DB_PATH=/tmp/runed.db`, `RUNTIME=local`, `IPC_DIR=/tmp/ipc`,
> a stub `AUTHD_URL` returning a fixed downscoped token, and a stub
> `ROUTD_URL` accepting `/v1/turns/*`. Accepts a stub
> `POST /v1/runs {folder:"demo", message_batch:"...", capability_scopes:[...]}`,
> stands up a unix MCP socket, brokers a (faked) capability token, spawns a
> `LocalRuntime` "container" that connects to the socket and submits a
> turn, and returns `{run_id, outcome:"ok", session_id}`. No
> arizuko-domain hardcoding beyond `types.*` (`types.Folder`,
> `types.UserSub`, `types.Scope`); no `core.Group`/`core.Folder` import in
> the binary's go-list output beyond `<daemon>/api/v1/` packages.

`LocalRuntime` runs the agent binary directly — no docker-in-docker — so
the test runs in CI. `FakeRuntime` (next to the `ContainerRuntime`
interface) backs unit tests of the queue + envelope without spawning
anything.

## `runed/api/v1/` — the published contract

`runed/api/v1/` ships the wire types (`RunRequest`, `RunOutcome`,
`RunStatus`, `SpawnRequest`, `SessionRow`) + a thin HTTP client, zero
arizuko-internal imports beyond `types/`. `routd` imports `runed/api/v1/`
to call `POST /v1/runs`; `runed` imports `routd/api/v1/` to call
`/v1/turns/*`. The mutual import is the published-contract convention, not
a cycle (both depend only on `types/`). The contract carries annotated
OpenAPI (`x-mcp-*`) so the agent-facing MCP surface derives from the REST
face; until [`11/18-openapi-mcp.md`](../11/18-openapi-mcp.md) lands the
MCP registrations are hand-rolled (carried from `ipc/ipc.go`).

## Touches

> Extracting `ipc/` here absorbs **`5/6` 6/6a** — the MCP-authz middleware
> chain (`granted`/`regSocial` → one `gated(Authorize)` wrapper, per
> `4/9`). Factor it as the host is built; `5/6` is the design reference.

| Component                      | Change                                                                                                             |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------ |
| `runed/` (new daemon)          | queue + container + ipc folded in; owns `runed.db` + `migrations/`; serves `/v1/runs`, `/v1/sessions`.             |
| `runed/api/v1/` (new)          | wire types + thin client for `POST /v1/runs` etc.                                                                  |
| `queue/`, `container/`, `ipc/` | move under `runed`'s ownership; `GatedFns` message funcs repointed at routd `/v1/turns/*` HTTP forwards.           |
| `routd` (sibling split)        | serves `/v1/turns/{turn_id}/*`; calls `runed POST /v1/runs`; owns `groups`/`routes`/messages.                      |
| `authd`                        | downscope endpoint brokers each agent token (5/1).                                                                 |
| `compose/compose.go`           | seed `service:runed` key + `service_scope` from `template/services/runed.toml`; `[[proxyd_route]]` for `/v1/runs`. |
| `store/migrations/`            | one-shot copy of `session_log` → `runed.db`; `sessions` → `routd.db` (E-routd); drop sources (big-bang cutover).   |

## Code pointers

- `queue/queue.go` — the `GroupQueue` scheduler: per-folder serialization,
  concurrency cap, folder-exclusivity, circuit breaker, `SendMessages`
  steer-into-running-container.
- `container/runner.go` — the per-turn Docker runner: `Run`, mounts,
  egress, idle/soft/hard deadlines. `container/runtime.go` — `Bin`,
  `StopContainerArgs`; the `ContainerRuntime` seam.
- `ipc/ipc.go` — the MCP host (folded-in `mcpd`): `ServeMCP`, the agent
  tool surface, `submit_turn`, `SO_PEERCRED` gate. `ipc/connector.go` —
  MCP-connector subprocess federation.
- `store/sessions.go` — `session_log` read/write
  (`RecordSession`/`EndSession`/`RecentSessions`; migration source →
  `runed.db`). The `sessions`/topic-lineage methods migrate to `routd`.
- [`1-auth-standalone.md`](1-auth-standalone.md) — `authd` downscope,
  `auth.ServiceToken`, `auth.VerifyHTTP`.
- [`U-genericization.md`](U-genericization.md) — Phase C gated split,
  `ContainerRuntime` seam, DB-ownership rule.

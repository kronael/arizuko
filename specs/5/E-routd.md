---
status: shipped
depends:
  [
    Q-unified-routing,
    B-route-mode-ingestion,
    F-topic-lineage,
    G-engagement,
    L-mention-promotion,
    W-webhook-routes,
    S-jid-format,
    1-auth-standalone,
    17-openapi-mcp,
  ]
---

# routd: the conversation state machine

**Decided** (operator + oracle, 2026-05-29; shipped 2026-06). `routd`
owns \*\*routing rules + the message/event store + the orchestration loop

- channel ingress/egress\*\*, carved out of the deleted `gateway/` +
  `api/`. `runed` ([`P-runed.md`](P-runed.md)) is a pure container
  spawner. `gated` is gone (v0.50.0).

## The decisions

**routd is the sole appender.** Every `messages` row — inbound from an
adapter, outbound from an agent, delegation hop, synthetic proactive
trigger — is written by routd and nobody else. That is what lets one
cursor (`chats.agent_cursor`) stand for "what the agent has seen": with
two writers the cursor and the row sequence drift.

**The atomic unit is the chat.** "Append inbound → resolve route →
start/continue turn" is one ordered view per `(chat, group)`.

**routd hosts the per-turn agent MCP socket in-process**
(`routd/mcp.go` `ServeTurnMCP` → `ipc.ServeMCP`, called per turn from
`routd/dispatch.go`). The originally-specced alternative — runed hosts
the socket and federates the conversation tools back to routd over
`/v1/turns/*` — was **descoped and never built**. runed only spawns the
container (`container.Input.ExternalMCP:true`) and mounts the ipc dir
the socket lives in. The `/v1/turns/*` REST handlers still exist as the
documented twin of those tools ([`17-openapi-mcp.md`](17-openapi-mcp.md)),
and runed still calls them; they are not a federation layer.

**Hard boundaries.** routd never spawns a container or holds a Docker
handle — it calls `POST <RUNED_URL>/v1/runs`. runed never appends a
message. routd is a token _verifier_, never a signer.

## routd.db

routd owns `routd.db` and migrates it (`routd/migrations/*.sql`). The
schema is the migrations — read them, not a copy here. Times are
RFC3339Nano UTC TEXT throughout; the Go layer computes every timestamp
(no `strftime` in SQL, which would diverge the format).

The tables and who else reads them:

| Table                               | What it carries                                                          |
| ----------------------------------- | ------------------------------------------------------------------------ |
| `groups`                            | folder = identity; `container_config`/`model` opaque, forwarded to runed |
| `chats`                             | per-chat `agent_cursor`, sticky group/topic pins                         |
| `messages` (+ `messages_fts`)       | the single event log; FTS5 shadow backs `find_messages` (5/C)            |
| `routes`                            | the route table (5/B, 5/Q)                                               |
| `sessions`                          | per-`(folder, topic)` session id, fork lineage, observed cursor (5/F)    |
| `chat_reply_state`                  | thread anchor + engagement deadline (5/G)                                |
| `turn_context` / `turn_results`     | turn binding + the submit_turn idempotency ledger                        |
| `cost_log`                          | per-turn model cost — routd persists what runed reports                  |
| `web_routes`                        | URL-tree access map (5/V)                                                |
| `route_tokens`                      | `/chat/`,`/hook/` bearer tokens, stored hashed (5/W)                     |
| `acl` / `acl_membership`            | routd is the policy decision point (5/32)                                |
| `idempotency_keys`                  | the replay ledger (§ Idempotency)                                        |
| `chat_proactive`                    | per-chat proactive cooldown (5/6)                                        |
| `group_watchers`, `system_messages` | cross-folder ambient (5/F); pending system events                        |

`session_id` is **opaque to routd** — runed produces it, routd persists
it via `submit_turn`. `pane_sessions` is read by the prompt build but
owned by runed; routd reads it over runed's `/v1/*`, never by direct SQL.

Every core daemon runs as UID 1000 with the whole data dir mounted rw
(`compose/compose.go:811-812` — `user: '1000:1000'` plus an unqualified
`dataDir:/srv/app/home` volume), so "owner" is a convention enforced by
review, not by the filesystem.

## The orchestration loop

`routd/loop.go` + `routd/dispatch.go`. Poll-driven over `routd.db`;
ingress (`POST /v1/messages`) writes the row then enqueues the chat.

- `pollOnce` (`loop.go:490`) — batch new rows by `chat_jid`, resolve,
  enrich attachments, steer-or-enqueue.
- `processGroupMessages` (`dispatch.go:22`) — the queue worker,
  serialized per folder. `web:` chats dispatch one turn per topic
  (`groupByTopic`); everything else one turn per distinct sender
  (`groupBySender`). Topics in one web chat do **not** share a session.
- `runTurn` (`dispatch.go:113`) → `dispatchRun` (`dispatch.go:504`) —
  the `POST /v1/runs` call.
- `steer` (`routd/steer.go:21`) runs in the **worker**, not only in the
  poll backstop. Adapter ingest enqueues straight into the worker, so
  when steering lived only in `pollOnce` it raced the queue and lost — a
  Slack `/root mint …` reached the agent as plain unelevated text and
  the elevation never fired (marinade atlas, 2026-07-16).

### Route resolution (`loop.go:610` `resolve`)

One function, called by both the poll and the worker — the single source
of truth for "which group owns this chat":

1. **Direct address.** `web:<folder>` and bare-folder JIDs (no `:`)
   matching a registered group resolve directly; the route table does
   not apply (`directFolder`).
2. **Route table.** `router.ResolveRouteTarget` scans by ascending
   `seq`, first match wins; `core.ParseRouteTarget` splits the target
   fragment into `{Folder, Topic, Mode}`. `match` keys: `platform`,
   `room`, `chat_jid`, `sender`, `verb`; glob is `path.Match`
   (`*` does not cross `/`), `key=` means "field absent", an omitted key
   is unconstrained.
3. **Engagement override** (5/G). An engaged `(jid, topic)` **overrides**
   the route table entirely — including `#observe` and routes pointing
   elsewhere — and also rescues a route miss. Once the bot has replied in
   a thread the user expects a live conversation; silencing it mid-thread
   is worse than the operator's original intent to reduce cold-message
   noise.
   **Topic-root normalization is load-bearing**: engagement is recorded
   on the root topic (`topic=""`) when the agent answers an @mention, but
   thread replies arrive with `topic="<thread>"`. `resolve` falls back to
   the root topic when the thread topic has no record of its own —
   without that, every threaded follow-up misses its own engagement.
4. **A transient `Routes()` read failure is logged loud, then treated as
   a route miss** — retrying in the resolver risks a poison-message loop,
   but a silent drop is indistinguishable from a clean miss.

5/L promotion happens at **ingress**, not here: an inbound replying to a
bot row, or landing in a thread the bot participated in, becomes
`verb=mention` before the row is written, so routing sees one uniform
trigger signal across adapters.

### Concurrency model (PINNED)

**routd is a single process per instance**, not multi-instance/HA. The
loop, the per-folder queue, and the in-memory turn state are
process-local. Two routd processes against one `routd.db` is
unsupported. No DB job-claim protocol: the in-memory queue (keyed by
destination folder via `folderForJid`) is the single arbiter;
`agent_cursor` recovery handles crash restart. Removing this constraint
is a future spec.

### Consistency contract

- **Serialized per folder.** At most one turn per folder. Concurrent
  inbounds are absorbed into the running turn (steered) or queued behind
  it; duplicate enqueues for one chat collapse.
- **Per-turn callback serialization.** routd serializes
  `/v1/turns/{turn_id}/*` append-and-deliver per `turn_id`. Each new
  `reply` chains `reply_to_id` to the prior reply's `platform_id`; while
  the prior reply is still `pending` it chains to the prior internal
  `message_id`, and the retry loop rewrites the anchor when the
  `platform_id` lands.
- **Engagement is NOT claimed at ingress** (`routd/server.go:485`). The
  owning folder is unresolved until route resolution runs, and a
  pre-`PutMessage` claim with an empty folder makes `Engaged` return
  `("", true)` and misroute. routd claims at dispatch, with the resolved
  folder.
- **Observed-cursor advance** (5/B, 5/F) is per-`(folder, topic)`,
  written during prompt construction, **not** transactional with the run.
  At-least-once: a crash may re-show an observed message next turn
  (benign — the prompt rule says don't act on observed).
- **Cursor recovery.** `agent_cursor` advance is not transactional with
  the run either. On crash mid-turn the chat is re-fed from the
  un-advanced cursor (`recoverPending`, `loop.go:414`); `turn_results`
  PK `(folder, turn_id)` dedups duplicate `submit_turn` callbacks.
- **Outbound is poll-reconciled.** A bot row is written `status='pending'`,
  delivered inline, marked `sent` on success; `maybeRetryOutbound`
  (`loop.go:358`) re-dispatches rows older than 30s and fails them after
  24h. **Dedup is the adapter's contract**: routd passes the bot row's
  stable `message_id` as the delivery idempotency key on every resend.
  Delivery never blocks the turn.

## HTTP + MCP surface

Routes: `routd/server.go:232-287`. Wire types: `routd/api/v1/` (imports
only `types/`). Every endpoint has an MCP twin where an agent needs it —
one hand-written handler, two faces
([`17-openapi-mcp.md`](17-openapi-mcp.md)). Errors are
`{"error":"<code>","message":"<human>"}`; `/health` and `/openapi.json`
are public, everything else auth-gated.

Groupings: ingress `POST /v1/messages`; egress passthrough
`POST /v1/outbound` (timed/onbod — resolves the delivering adapter,
appends nothing, the caller owns its row); reads
`/v1/messages/{inspect,thread,find}` and `/v1/routing/*`; the turn
callbacks `/v1/turns/{turn_id}/*`; and the resreg-backed resources
(routes, route tokens, web routes, tasks, secrets, ACL) whose REST and
MCP faces are derived from one registration
(`routd/*_resource.go` + `resreg/resources/*.go`).

**Cold-tier resources are resreg registrations, not hand-rolled ipc
tools.** `add_route`/`set_routes`/`delete_route`/`list_routes` and
`issue_chat_link`/`issue_webhook`/`list_route_tokens`/`revoke_route_token`
moved to the resreg seam (5/16); the tool names survive via
`Resource.MCPNames`. The hot-tier conversation tools
(`reply`/`send`/`send_file`/`inspect_messages`/`get_thread`/
`fetch_history`/`find_messages`/`like`/`edit`/`delete`/`pin_message`/
`unpin_message`/`unpin_all`/`engage`/`disengage`/`fork_topic`) stay
hand-authored in `ipc/ipc.go` — they are per-turn agent actions with no
operator REST twin worth deriving.

`reply` and `send` keep distinct names and sharp descriptions (threaded
answer vs. fresh top-level message) rather than one tool with a `mode=`
param, per the project tool-naming rule.

**Tool name ≠ path tail, in four places** (`ipc/ipc.go`): `send_file` →
`/document`; `dislike` → `/like` with `reaction="👎"` (there is no
`/dislike` endpoint — one code path per mechanism, both verbs visible to
the agent); `pin_message`/`unpin_message` → `/pin`,`/unpin`;
`unpin_all` → `/unpin` with `all:true`.

`get_thread` and `find_messages` read the DB in-process; their REST
twins are `GET /v1/messages/thread` and `/v1/messages/find` — **not**
`/v1/turns/{turn_id}/…`, which has no thread or find route.

### Idempotency (PINNED)

Ledger: `idempotency_keys`, implementation `routd/tokens.go:149`,
claim/commit `routd/db.go:426`. Two rules earn their lines because
getting either wrong is silent and expensive:

- **`endpoint` is the path TEMPLATE with variables collapsed**
  (`POST /v1/turns/reply`), never the filled path. The `{turn_id}`
  segment is per-turn, so embedding it partitions one logical command
  across turns and defeats the dedup entirely.
- **Canonical body** = the request bytes re-marshalled with sorted keys
  and no insignificant whitespace, so encoder differences don't produce
  false `409 idempotency_key_reuse`.

Per-surface: `POST /v1/messages` dedups on the message `id` (first-written
row is authoritative — the platform id is immutable and the log is
append-only); turn commands (`reply`/`send`/`document`) **require**
`X-Idempotency-Key` and replay the stored response without re-delivering;
mutations honor it when present. Rows expire 24h after `created_at`.

### Turn lifecycle

The agent calls `submit_turn` once at end of run (a hidden JSON-RPC
method, not in `tools/list`; REST twin `POST /v1/turns/{turn_id}/result`,
`routd/turns.go`). routd inserts into `turn_results` (PK
`(folder, turn_id)`); a duplicate returns `recorded:false`. On a first
record it recovers `(folder, topic, chat_jid)` from `turn_context`
(the payload carries no topic), persists `session_id`, writes one
`cost_log` row per model, publishes `round_done`, and delivers `result`.

**`round_done` is keyed on the chat JID's folder, not the routing target**
(`routd/turns.go:707`). When a routing rule maps `web:X/submissions →
groupY` those differ, `round_done` never arrives, and the form UI hangs.
That was a live bug; the key is `strings.TrimPrefix(tc.ChatJID, "web:")`.

**Completion reconciliation.** Two terminal signals exist: the
`POST /v1/runs` HTTP response and `submit_turn`.

| Arrival                                               | Resolution                                                                                     |
| ----------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `submit_turn` then run-response                       | `submit_turn` authoritative for `session_id`/`status`; run-response only marks the turn done   |
| run-response `ok`/`silent`, no `submit_turn`          | run-response's `session_id` persists; cursor advances; turn closed                             |
| run-response `error`, then late `submit_turn:success` | FIRST terminal signal wins for cursor + delivery; the late one records but does not re-deliver |
| neither (crash)                                       | state stays `running`; restart re-feeds from the un-advanced cursor; `turn_results` dedups     |

`agent_cursor` advances **once**, at the first terminal signal. A
duplicate `POST /v1/runs` start for an already-done `turn_id` →
`409 turn_done` (`routd/turns.go:202`). Post-terminal callbacks stay
valid until the run-response returns — the run may still emit trailing
frames, and per-folder serialization guarantees no second turn started.

**Stale `running` rows sweep to `'expired'`, NOT `'done'`**
(`routd/db.go:955` `SweepExpiredRunning`). This is load-bearing and
non-obvious: crash-recovery keys re-feed on `state='running'` (sweeping
to `done` would silently kill replay for a turn that never completed),
while the double-live-run guard keys on `state='done'` (sweeping to
`done` would falsely 409 a legitimate re-dispatch). `'expired'` is
neither.

Turn retry on a run that dies without replying:
[`12-turn-retry.md`](12-turn-retry.md).

## The routd↔runed interface (PINNED)

`POST <RUNED_URL>/v1/runs`, wire types `runed/api/v1/types.go`. routd
drives the run; runed runs it. **Synchronous for the turn boundary** —
routd blocks on the HTTP response; the agent's conversation frames
arrive out-of-band during the run via the in-process MCP socket.
`submit_turn` is the canonical end-of-turn signal; the response carries
`outcome` + `session_id` as a **backstop** when the agent never called it.

Three discriminators on the response, each meaning something different:

- **`outcome`** — `ok` (ran; advance, record), `error` (run failed;
  advance past the batch, mark errored, notify), `silent` (ran, nothing
  to deliver — logged, not an error).
- **`busy`** — runed did **not** admit the run (folder busy with a dead
  container, or the global `MAX_CONCURRENT_CONTAINERS` cap) and keeps no
  internal queue. A **retryable reject, not a turn boundary and not a
  failure**: do not advance the cursor, do not count it toward the queue
  circuit breaker, leave `turn_context` running so the re-dispatch is
  live. Because runed's cap is the same one that bounds routd's queue,
  the poll re-feed clears it within one `POLL_INTERVAL`.
- **`steered`** — a steer ack: the original run governs the batch, so
  don't advance.

**Transport failure ≠ `outcome:error`.** On a transport failure routd
does not know whether the run happened, so it does not advance the
cursor and the chat is re-fed next poll (at-least-once; `turn_results`
dedups). A clean `200 {outcome:error}` means the run definitively failed
— advance past the batch, no infinite replay. `POST /v1/runs` is never
blindly retried; re-attempt is the normal poll re-feed under per-folder
serialization.

`RUNED_RUN_TIMEOUT` is routd's hard deadline; on expiry routd cancels
the request and runed `Stop`s the container on context cancel. Cancel is
request-scoped only — there is no `DELETE /v1/runs/{run_id}`. `run_id` is
for log correlation; run idempotency keys on `turn_id`.

## Auth

routd holds no signing key. It offline-verifies against authd's cached
JWKs via `auth/` ([`1-auth-standalone.md`](1-auth-standalone.md)).
Three credential classes cross its boundary; keep them separate:

| Credential             | Issued by | Verified how                                  | Used for                                     |
| ---------------------- | --------- | --------------------------------------------- | -------------------------------------------- |
| Agent capability token | authd     | `auth.VerifyHTTP` offline against cached JWKs | `/v1/turns/*`, agent-side resource calls     |
| Adapter/service token  | authd     | same offline verify; `sub = service:<daemon>` | `/v1/messages`, `/v1/outbound`, registration |
| **Route token** (5/W)  | **routd** | `sha256(token)` lookup in `route_tokens`      | webd's `/chat/<token>/`, `/hook/<token>`     |

A route token is a 32-byte opaque secret stored hashed. It is not a JWT,
carries no scope, authd never sees it, and it authorizes exactly "append
at this JID" — nothing else. Verifying one is never a path to the other.

Authorization is one evaluator over ACL rows, deny-wins
(`auth/authorize.go:25`, spec [`32-acl-unified.md`](32-acl-unified.md)).
There are no depth-derived tiers and no tier fallback.

routd obtains its own `service:routd` token at boot
(`auth.ServiceToken`) for daemon→daemon calls. The HMAC
`CHANNEL_SECRET` / `PROXYD_HMAC_SECRET` paths are retired.

## Code pointers

- `routd/loop.go`, `routd/dispatch.go` — the loop, resolution, dispatch.
- `routd/server.go` — ingress, the `/v1/turns/*` mux, 5/L promotion.
- `routd/turns.go` — turn callbacks, idempotency wrapper, `submit_turn`.
- `routd/steer.go` — sticky nav, slash commands, `@child` delegation.
- `routd/prompt.go` — `buildAgentPrompt`, `paneHints`, `linkContextBlock`.
- `routd/mcp.go` — `ServeTurnMCP`, the per-turn socket.
- `routd/db.go`, `routd/migrations/` — schema + access.
- `router/router.go`, `core.ParseRouteTarget` — match + target parsing.
- `tests/standalone/routd_test.go` — the standalone-readiness bar: boots
  on its own DB with a stub `RUNED_URL`, runs its own migrations,
  ingests → resolves → dispatches → records, with no `core.Folder` leak
  beyond `types.*`.
- [`P-runed.md`](P-runed.md) — the `POST /v1/runs` peer.

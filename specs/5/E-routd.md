---
status: shipped
depends:
  [
    U-genericization,
    Q-unified-routing,
    B-route-mode-ingestion,
    F-topic-lineage,
    G-engagement,
    L-mention-promotion,
    W-webhook-routes,
    S-jid-format,
    1-auth-standalone,
    5-uniform-mcp-rest,
  ]
---

# routd: the conversation state machine (gated split)

> **Status note (2026-05-30).** The build flipped the MCP-host
> ownership: **routd hosts the per-turn agent MCP socket in-process**
> (`routd/mcp.go` `ServeTurnMCP` → `ipc.ServeMCP`, called per-turn from
> `routd/dispatch.go`). `runed` became a **pure container-spawner**
> (`runed/docker.go`, `container.Input.ExternalMCP:true`) — it hosts no
> socket and forwards no conversation tools. The "runed federates the
> conversation tools back to routd's `/v1/turns/*`" HTTP model below was
> **descoped and never built**. Read every "served via runed's
> federation" / "Served by: routd (via runed)" / "routd hosts no agent
> MCP socket" reference as: routd serves the tools in-process; runed
> only spawns the container. The REST wire contract (`POST /v1/runs`,
> the `/v1/turns/*` shapes) still documents the real split surface.

**Decided** (operator + oracle, 2026-05-29). `routd` is carved out of
`gated` and owns **routing rules + the message/event store + the
orchestration loop + channel ingress/egress**. It is the **sole
appender of messages** — every inbound from an adapter, every outbound
from an agent, every delegation hop, lands as a `routd`-owned `messages`
row written by `routd` and nobody else. The atomic unit is the chat:
"append inbound → resolve route → start/continue turn" share one
transactional view per `(chat, group)`.

`status: shipped` (2026-06). The loop was extracted verbatim from the
former `gateway/gateway.go` + `api/api.go` (now deleted) + `store/` +
`router/` into this standalone daemon. routd shipped in the gated-split
release alongside `runed` (the `authd` daemon shipped standalone first;
[`U-genericization.md`](U-genericization.md) § Phase C, § HMAC
retirement). It landed as one coordinated migration: the daemons carved
their tables into their own DBs (`routd.db`/`runed.db`), the monolithic
`messages.db` schema-authority was deleted, no shared-DB interim, no compat shim
([`U-genericization.md`](U-genericization.md) NO BACKWARD COMPATIBILITY).
The agent MCP socket is hosted **in-process by routd**
(`ServeTurnMCP`) — no separate MCP-host daemon and no runed federation
(see Status note). `runed` ([`P-runed.md`](P-runed.md)) only spawns the
container and mounts the ipc dir routd's socket lives in.

## What routd owns vs. what it doesn't

| Concern                                                    | Owner                                |
| ---------------------------------------------------------- | ------------------------------------ |
| Routing rules, message/event store, orchestration loop     | `routd`                              |
| Channel ingress (`POST /v1/messages`) + egress (delivery)  | `routd`                              |
| Token signing, JWKs, OAuth login                           | `authd`                              |
| Container lifecycle, per-spawn state, the agent run itself | `runed`                              |
| Per-turn agent MCP socket (in-process), conversation tools | `routd` (`ServeTurnMCP`)             |
| Container spawn/steer/teardown, capability tokens          | `runed` ([`P-runed.md`](P-runed.md)) |

Hard boundaries, no overlap:

- **routd never spawns containers.** It calls `runed` over HTTP
  (§ The routd↔runed interface) and never touches Docker.
- **runed never appends messages.** The agent's `reply`/`send`/`edit`
  tools are served by **routd's in-process MCP socket** (`ServeTurnMCP`)
  and deliver through routd's own Deliverer — routd is the sole
  appender. (The REST `/v1/turns/{turn_id}/*` shapes below document that
  same handler's wire face; the originally-specced runed→routd federation
  forward was descoped — see Status note.)
- **routd is a token verifier, not a signer.** It offline-verifies agent
  capability tokens against authd's JWKs (§ Auth). Route-tokens (the
  `/chat/`,`/hook/` widget tokens) are a **distinct** credential routd
  owns outright.
- **routd hosts the per-turn agent MCP socket in-process**
  (`ServeTurnMCP` → `ipc.ServeMCP`). The unix socket per folder is
  routd's; runed only spawns the container that connects to it
  (`ExternalMCP`). routd's MCP face is the routing-control tools **and**
  the conversation-command tools, all served directly in one process
  (one handler, two faces — § MCP tool face).

## routd.db schema

`routd` owns `routd.db` — its own SQLite file (WAL), its own
`routd/migrations/` subdir, per the
[`U-genericization.md`](U-genericization.md) DB-ownership rule. The
tables below carry the live column shapes out of today's `messages.db`
(`store/migrations/*.sql`). **Times are RFC3339Nano UTC TEXT
throughout** — the Go layer computes every timestamp (no `strftime` in
SQL, which would diverge the format). The cutover copies the listed
tables' rows out of `messages.db`, drops them from the source, one-shot,
no dual-write.

### Group identity — `groups`

Path IS identity (`parent` = `filepath.Dir(folder)`, display =
`filepath.Base(folder)`).

```sql
CREATE TABLE groups (
  folder                  TEXT PRIMARY KEY,         -- path IS identity ("krons", "atlas/support")
  added_at                TEXT NOT NULL,
  container_config        TEXT,                     -- JSON GroupConfig; opaque to routd, forwarded to runed
  updated_at              TEXT,
  product                 TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day  INTEGER NOT NULL DEFAULT 0,
  open                    INTEGER NOT NULL DEFAULT 1,  -- 1 = visible to open siblings as ambient source (5/F)
  observe_window_messages INTEGER,                  -- per-group cap; NULL = inherit env default
  observe_window_chars    INTEGER,                  -- per-group cap; NULL = inherit env default
  model                   TEXT                      -- per-group model override; NULL = instance default
);
```

`container_config` and `model` are forwarded to runed at run time; routd
treats them as opaque (it does not interpret mounts or timeouts).

### Chats — per-chat cursor + sticky state

```sql
CREATE TABLE chats (
  jid          TEXT PRIMARY KEY,     -- prefix:identifier | bare folder | web:<f> | hook:<f>/<src>
  errored      INTEGER NOT NULL DEFAULT 0,
  agent_cursor TEXT,                 -- RFC3339Nano; high-water mark of messages fed to the agent
  sticky_group TEXT,                 -- @-prefix routing pin (5/Q)
  sticky_topic TEXT,                 -- #-prefix topic pin
  is_group     INTEGER NOT NULL DEFAULT 0  -- set per inbound (SetChatIsGroup)
);
```

### Messages — the single event log (sole-append)

The one table all inbound and outbound flow through; routd is its only
writer.

```sql
CREATE TABLE messages (
  id              TEXT PRIMARY KEY,    -- unique across the table; cross-adapter IDs cannot collide
  chat_jid        TEXT NOT NULL,
  sender          TEXT NOT NULL,       -- platform sub | folder (outbound) | "delegate" | "system" | "timed-*"
  sender_name     TEXT,
  content         TEXT NOT NULL,
  timestamp       TEXT NOT NULL,
  is_from_me      INTEGER NOT NULL DEFAULT 0,
  is_bot_message  INTEGER NOT NULL DEFAULT 0,  -- drives 5/L reply-to-bot → mention promotion
  forwarded_from  TEXT,                -- delegation/escalation return address (5/Q)
  reply_to_id     TEXT,
  reply_to_text   TEXT,
  reply_to_sender TEXT,
  topic           TEXT NOT NULL DEFAULT '',
  routed_to       TEXT NOT NULL DEFAULT '',  -- folder this row was routed/delivered to
  verb            TEXT NOT NULL DEFAULT 'message',  -- message|mention|like|dislike|edit|delete|join|untrusted|...
  attachments     TEXT NOT NULL DEFAULT '',  -- JSON []InboundAttachment (cleared after enrich)
  source          TEXT NOT NULL DEFAULT '',  -- adapter that handled the row (inbound: receiver; outbound: deliverer)
  is_observed     INTEGER NOT NULL DEFAULT 0, -- stored under an observe-mode route; no turn fired (5/B)
  turn_id         TEXT,                -- outbound: the inbound message id that produced this row
  status          TEXT NOT NULL DEFAULT 'sent',  -- sent|pending|failed (poll-based outbound delivery)
  platform_id     TEXT,                -- platform-native id (Slack ts, Telegram msg_id) for outbound
  chat_name       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_messages_chat_ts ON messages(chat_jid, timestamp);
CREATE INDEX idx_messages_observed ON messages(routed_to, is_observed, timestamp);
CREATE INDEX idx_messages_turn_id ON messages(turn_id) WHERE turn_id IS NOT NULL;
CREATE INDEX idx_messages_status ON messages(status) WHERE status != 'sent';

-- FTS5 shadow over content; kept in sync by AI/AU/AD triggers. Backs
-- get_history search / find_messages. Keyed on the implicit rowid.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  content, content='messages', content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);
-- + messages_fts_ai / _au / _ad triggers (see store/migrations/0070).
```

### Routes — the route table

```sql
CREATE TABLE routes (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  seq                      INTEGER NOT NULL DEFAULT 0,  -- priority; lower seq wins, scanned in order
  match                    TEXT    NOT NULL DEFAULT '', -- space-separated key=glob predicates
  target                   TEXT    NOT NULL,            -- folder | folder#observe | folder#<topic> (core.ParseRouteTarget)
  observe_window_messages  INTEGER,                     -- per-route cap (wins over per-group)
  observe_window_chars     INTEGER
);
CREATE INDEX idx_routes_seq ON routes(seq);
```

`match` keys: `platform`, `room`, `chat_jid`, `sender`, `verb`. Glob is
`path.Match` (`*` does not cross `/`); `key=` means "field absent";
omitting a key leaves it unconstrained (`router.RouteMatches`).

### Topic lineage + sessions — `sessions`

Per-`(folder, topic)` session id + fork lineage (5/F). routd owns the
lineage columns; `session_id` is **opaque to routd** — runed produces it
and routd persists it via `submit_turn` (§ turn lifecycle).

```sql
CREATE TABLE sessions (
  group_folder    TEXT NOT NULL,
  topic           TEXT NOT NULL DEFAULT '',
  session_id      TEXT NOT NULL,
  parent_topic    TEXT,                   -- *string: "" = fork from main, NULL = no parent
  forked_at       TEXT,
  observed_cursor TEXT,                   -- per-topic watermark over is_observed messages
  PRIMARY KEY (group_folder, topic)
);
CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);
```

### Engagement — `chat_reply_state`

Last-reply threading anchor + the 5/G engagement deadline, per
`(jid, topic)`.

```sql
CREATE TABLE chat_reply_state (
  jid            TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  last_reply_id  TEXT NOT NULL,            -- thread anchor for reply-by-default
  engaged_until  TEXT,                     -- RFC3339Nano deadline; NULL = idle (5/G)
  engaged_folder TEXT NOT NULL DEFAULT '', -- folder claiming the engagement; resolves routing fallback
  PRIMARY KEY (jid, topic)
);
```

### Turn context + results — `turn_context`, `turn_results`

`turn_context` binds a `turn_id` to its `(folder, topic, chat_jid)` at
run-start so the callbacks (`/v1/turns/{turn_id}/*`) and `submit_turn`
(which carries no `topic`) recover the active topic from `turn_id` alone.
Written in the dispatch tx; survives restart, so a late `submit_turn`
after a routd bounce still resolves its topic.

```sql
CREATE TABLE turn_context (
  turn_id        TEXT PRIMARY KEY,        -- the triggering inbound's message id
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  chat_jid       TEXT NOT NULL,
  trigger_sender TEXT NOT NULL,           -- for the timed-* engagement-skip
  started_at     TEXT NOT NULL,
  run_id         TEXT,                    -- runed's run id once POST /v1/runs returns
  state          TEXT NOT NULL DEFAULT 'running'  -- running | done | expired
);
```

`turn_results` is the idempotency ledger for agent-submitted turn
outcomes (§ turn lifecycle); PK `(folder, turn_id)` is the dedup key.

```sql
CREATE TABLE turn_results (
  folder       TEXT NOT NULL,
  turn_id      TEXT NOT NULL,
  session_id   TEXT,
  status       TEXT NOT NULL,            -- success | error
  recorded_at  TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id)
);
```

### Cost ledger — `cost_log`

Per-turn model cost. **routd owns it**: cost is per-turn and routd owns
turns/messages, so the turn-outcome callback persists it. `runed` does
**not** persist cost — it **reports** the per-model breakdown in the
`submit_turn` → `/v1/turns/{id}/result` payload (`models`), and routd
writes one `cost_log` row per `(folder, turn_id, model)` from the
`/result` handler (same first-record path that writes `turn_results`,
under the `(folder, turn_id)` dedup, so a duplicate `submit_turn` does not
double-charge). `cost_cap_cents_per_day` (groups) reads this table.

```sql
CREATE TABLE cost_log (
  folder       TEXT NOT NULL,
  turn_id      TEXT NOT NULL,
  model        TEXT NOT NULL,            -- e.g. "claude-…"
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_cents   INTEGER NOT NULL DEFAULT 0,
  recorded_at  TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id, model)
);
CREATE INDEX idx_cost_log_folder_day ON cost_log(folder, recorded_at);
```

### Web routes — `web_routes`

URL-tree access map for the operator web layer (5/V). Single writer;
group removal cascades.

```sql
CREATE TABLE web_routes (
  path_prefix TEXT PRIMARY KEY,
  access      TEXT NOT NULL CHECK(access IN ('public','auth','deny','redirect')),
  redirect_to TEXT,
  folder      TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
  created_at  TEXT NOT NULL
);
```

### Route tokens — `route_tokens`

The `/chat/`,`/hook/` widget/webhook bearer tokens (5/W). Stored as
`sha256(token)`; raw token returned once at issue. `owner_folder`
cascades on group removal.

```sql
CREATE TABLE route_tokens (
  token_hash    BLOB PRIMARY KEY,        -- sha256(raw); raw returned once
  jid           TEXT NOT NULL,           -- web:<folder>[/...] | hook:<folder>/<source>[/...]
  owner_folder  TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
  created_at    TEXT NOT NULL
);
CREATE INDEX route_tokens_jid ON route_tokens(jid);
```

### Ancillary — system messages, watchers, idempotency

```sql
-- Pending system events (new_day, new_session) flushed into the next
-- prompt. Enqueued by the loop, drained by buildAgentPrompt.
CREATE TABLE system_messages (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  folder  TEXT NOT NULL,
  source  TEXT NOT NULL,
  kind    TEXT NOT NULL,
  body    TEXT NOT NULL,
  created TEXT NOT NULL
);

-- Directional cross-folder ambient (5/F observe_group): observer receives
-- source's messages as <observed> context on its next turn.
CREATE TABLE group_watchers (
  observer TEXT NOT NULL,
  source   TEXT NOT NULL,
  PRIMARY KEY (observer, source)
);

-- Idempotency ledger (§ Idempotency). One row per (endpoint, key); stores
-- the original response so a replay returns the exact status+body without
-- re-executing. Survives restart. Swept hourly past expires_at.
CREATE TABLE idempotency_keys (
  endpoint     TEXT NOT NULL,            -- path TEMPLATE with vars collapsed: "POST /v1/turns/reply"
                                         --   NEVER the filled path ("POST /v1/turns/{turn_id}/reply") —
                                         --   the per-turn id would partition the ledger and break dedup.
  key          TEXT NOT NULL,            -- X-Idempotency-Key value
  request_hash TEXT NOT NULL,            -- sha256 of canonical request body; mismatch on replay → 409
  status       INTEGER NOT NULL,         -- stored HTTP status
  response     TEXT NOT NULL,            -- stored JSON body, replayed verbatim
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,            -- created_at + 24h
  PRIMARY KEY (endpoint, key)
);
CREATE INDEX idx_idempotency_expiry ON idempotency_keys(expires_at);
```

**Migration note.** routd runs its own `routd/migrations/*.sql` at
startup (same numbering as `store/migrations/`). The cutover copies the
above tables and drops the source. Tables NOT listed here (`secrets`,
`acl`, `acl_membership`, `scheduled_tasks`, `network_rules`,
`audit_log`, `config_meta`, `pane_sessions`, auth, onboarding) belong to
other split products or the residual gated. `pane_sessions` is read by
the loop's `paneHints` but **owned by runed** — routd reads it over
runed's `/v1/*`, never by direct SQL.

## The orchestration loop

Extracted verbatim from `gateway/gateway.go` (`pollOnce` /
`processGroupMessages` / `processSenderBatch`). Poll-driven over
`routd.db`; ingress (§ POST /v1/messages) writes the inbound row then
enqueues the chat.

```
pollOnce (every POLL_INTERVAL):
  msgs, hi := store.NewMessages(since)         # rows after last cursor, all chats
  group by chat_jid
  for each (chat_jid, chatMsgs):
    last := chatMsgs[-1]
    group, ok := resolveOrEngaged(chat_jid, last)   # § Route resolution
    if !ok:
      maybe enqueue onboarding (if ONBOARDING_ENABLED & platform allowed & not discord-guild-non-mention)
      advance agent_cursor; continue              # route miss → drop
    if handleStickyCommand(chat_jid, last): continue   # @group / #topic nav (5/Q)
    if handleCommand(last, group): continue            # gateway slash-commands
    if tryExternalRoute(...): advance; continue        # @child delegation / reply-chain target
    rt := router.ResolveRouteTarget(last, routes)
    effTopic := effectiveTopic(chat_jid, last.Topic)   # sticky_topic overrides
    if engaged(chat_jid, engTopic):                     # 5/G — engagement OVERRIDES route table
      group = EngagedFolder(chat_jid, engTopic)
    else if rt.Mode == "observe":                        # 5/B — silent ingest
      MarkMessagesObserved(rt.Folder, ids); advance; continue
    if rt.Topic != "": pin topic on chatMsgs            # route-pinned topic (5/F)
    enrichAttachments(...)                               # download media, whisper transcribe
    if queue.SendMessages(chat_jid, rendered):          # steer into a RUNNING container
      SetLastReply; BumpEngagement (unless timed-*); recordSteeredTs; continue
    queue.EnqueueMessageCheck(chat_jid)                  # else spawn a fresh run

processGroupMessages(chat_jid)  (queue worker, serialized per folder):
  agentTs := GetAgentCursor(chat_jid)
  msgs := MessagesSince(chat_jid, agentTs)
  group, ok := resolveOrEngaged(chat_jid, msgs[-1])     # same renderer as pollOnce
  if !ok: advance; return
  strip gateway-command rows
  if web: chat → processWebTopics (one run per topic)
  else group by sender → processSenderBatch each:
    buildAgentPrompt(group, topic, batch)               # sysMsgs+autocalls+persona+<observed>+feed (advances observed_cursor)
    SetLastReply(chat, topic, last.id, folder)          # seed reply-to-bot threading
    setCurrentTurn(folder, last.sender, topic)          # publish trigger for engagement-skip + reply fallback
    turn_context.put(turn_id=last.id, folder, topic, chat_jid, trigger=last.sender)
    out := dispatchRun(group, prompt, turn_id=last.id, ...)  # § routd↔runed; 409 turn_done if already terminal
    on error & no output: advance cursor PAST batch; mark errored; send failure notice
  advance agent_cursor
```

**Web chats (`processWebTopics`).** `web:` chats are processed per topic,
not per sender: routd groups pending rows by `topic` (first-seen order),
ensures each topic's lineage, builds one prompt per topic, dispatches one
run per topic in order. Each topic run advances the chat's `agent_cursor`
to that batch's tail and the topic's own `observed_cursor`; topics in one
web chat do **not** share a session (each `(folder, topic)` keeps its own
`sessions` row). A web-chat run serializes under the folder queue like
any other turn.

### Route resolution (`resolveOrEngaged`)

One renderer, two call sites (`pollOnce` + `processGroupMessages`) — the
single source of truth for "which group owns this chat":

1. **Direct address.** `web:<folder>` and bare-folder JIDs matching a
   registered group resolve directly; the route table does **not** apply.
2. **Route table.** `router.ResolveRouteTarget(msg, routes)` scans
   `routes` by ascending `seq`, returns the first whose `match`
   predicates all pass, parses the `target` fragment
   (`core.ParseRouteTarget` → `{Folder, Topic, Mode}`). @mention rows
   (`verb=mention`) stack above `#observe` catch-alls (5/B).
3. **Engagement override** (5/G). When `(jid, effTopic)` is engaged
   (`engaged_until > now`), the engaged folder **overrides** the route
   table entirely — including `#observe` and routes elsewhere. Checked
   before the `#observe` branch. On a route miss, the engagement fallback
   (`EngagedFolder`) still delivers to the last folder that spoke there;
   only a true idle miss drops.
4. **5/L promotion** happens at ingress, not here: an inbound whose
   `reply_to_id` points at a bot message (`is_bot_message=1`) is promoted
   to `verb=mention` before the row is written, so routing sees one
   uniform trigger signal across adapters.

### Concurrency model (PINNED)

**routd is a single process per instance**, NOT multi-instance/HA. The
loop, the per-folder queue, and the in-memory turn state
(`currentTrigger`, `inFlightTurns`, `steeredTs`) are process-local,
exactly as today's gateway. Two routd processes against one `routd.db` is
unsupported (design choice). No DB job-claim protocol: the in-memory
queue (keyed by destination folder via `folderForJid`) is the single
arbiter; `agent_cursor` recovery handles crash restart. Removing this
constraint is a future spec.

### Atomic / ordered-per-chat consistency contract

Invariant: **append inbound → resolve route → start/continue turn is one
ordered view per `(chat, group)`**, serial within a folder.

- **Single appender.** Every `messages` row is written by routd, so the
  cursor (`agent_cursor`) and the row sequence never diverge across
  writers.
- **Serialized per folder.** At most one turn per folder. Concurrent
  inbounds are absorbed into the running turn (steered) or queued behind
  it; duplicate enqueues for one chat collapse.
- **Per-turn callback serialization.** routd serializes
  `/v1/turns/{turn_id}/*` append-and-deliver **per `turn_id`** (a lock so
  out-of-order arrivals append in receive order). Reply chaining: each new
  `reply` chains `reply_to_id` to the prior reply's `platform_id`; if the
  prior reply is still `pending` (no `platform_id` yet), it chains to the
  prior **internal `message_id`**, and the retry loop rewrites the thread
  anchor when the `platform_id` lands. A replay of an earlier command
  after later ones succeeded returns the original stored response (no
  reorder).
- **Engagement writes move with the row.** Ingress writes `SetEngagement`
  for a `verb=mention` inbound **before** `PutMessage`, so row, routing
  decision, and engagement claim commit together. At outbound,
  `SetLastReply` (always) + `BumpEngagement` (unless `timed-*` trigger)
  write the same row.
- **Observed-cursor advance (5/B/5/F).** `sessions.observed_cursor` is
  **per-(folder, topic)**, advanced when `buildAgentPrompt` reads the
  observed window (newest observed timestamp written as the new cursor in
  the same step as prompt construction, before dispatch). NOT
  transactional with the run — at-least-once, so a crash may re-show an
  observed message next turn (benign: the prompt rule says don't act on
  observed).
- **Cursor recovery.** `agent_cursor` advance is **not** transactional
  with the agent run (in runed). On crash mid-turn the chat is re-fed from
  the un-advanced cursor (`recoverPendingMessages`); `turn_results` PK
  `(folder, turn_id)` dedups duplicate `submit_turn` callbacks.
- **Outbound is poll-reconciled.** A bot row is written `status='pending'`,
  delivery attempted inline, marked `sent` on success; `outboundRetryLoop`
  re-dispatches `pending` rows older than 30s, fails them after 24h.
  **Outbound dedup is the adapter's contract**: routd passes the bot row's
  stable `message_id` as the delivery idempotency key on every (re)send,
  and the adapter dedups platform-side — a redelivered `pending` row must
  not create a second platform message. Delivery never blocks the turn.

## api/v1 surface

`routd/api/v1/` is the published-contract package (wire types + thin
client), importing only `types/`
([`U-genericization.md`](U-genericization.md) § Per-service api/v1).
Every endpoint has an MCP twin where an agent needs it — one hand-written
handler, two faces ([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)).

All JSON errors use `{"error":"<code>","message":"<human>"}` with the
HTTP status carrying the class. `GET /openapi.json` and `GET /health`
are public; everything else is auth-gated (§ Auth).

**Idempotency (mandatory).** Every append/mutation endpoint accepts an
`X-Idempotency-Key` header; the dedup ledger is the `idempotency_keys`
table (persistent, survives restart). Pinned protocol:

1. Compute `request_hash = sha256(canonical-body)` and `INSERT` into
   `idempotency_keys` with a placeholder status. First writer wins (PK
   `(endpoint, key)` serializes concurrent retries). **`endpoint` is the
   path TEMPLATE with path variables collapsed** (`POST /v1/turns/reply`),
   **not** the filled path (`POST /v1/turns/{turn_id}/reply`): the
   `{turn_id}` segment is per-turn, so embedding it would partition one
   logical command across turns and defeat the dedup. The client-supplied
   `X-Idempotency-Key` is what scopes the command; the template names the
   operation. **Canonical body** =
   the request bytes re-marshalled with sorted object keys and no
   insignificant whitespace (Go: decode to `map[string]any` →
   `json.Marshal`), so encoder differences don't produce false 409s.
2. **Insert won** → execute the handler, then `UPDATE` the row with the
   real `(status, response)` in the **same tx** that appends the
   `messages` row. The committed row IS the durable record; a crash after
   commit but before replying is recovered by the retry replaying it.
3. **Insert lost** → if `request_hash` matches, replay the stored
   `(status, response)` verbatim without re-executing; if it **differs**,
   return `409 {"error":"idempotency_key_reuse"}`.
4. Rows expire 24h after `created_at`; hourly GC drops them. A key reused
   after expiry is a fresh request.

Per-endpoint key rules:

- **`POST /v1/messages`**: the dedup key is the **message `id`** (the PK),
  independent of `X-Idempotency-Key`. A duplicate `id` is a no-op insert
  returning `200` echoing the stored row — even if the body differs, the
  **first-written row is authoritative** (the platform id is immutable;
  silently keeping the first is correct for an append-only log).
  `X-Idempotency-Key` is honored only when `id` is **absent**; routd then
  mints `id = <adapter>-<idempotency-key>` so the two keys collapse.
  Sending a stable `id` and a different `X-Idempotency-Key` → `400
{"error":"ambiguous_idempotency"}`.
- **Turn commands** (`reply`/`send`/`document`): `X-Idempotency-Key`
  **required**. The stored `response` is the original
  `{message_id, platform_id, status}`; a replay returns it verbatim and
  does **not** append a second row or re-deliver. This is command-level
  dedup — the ledger row, not `turn_results` (which dedups only
  `submit_turn`).
- **Mutations** (`like`/`edit`/`delete`/`pin`/`unpin`, route CRUD,
  web-route, route-token): `X-Idempotency-Key` optional but honored via
  the same ledger; a missing key means at-least-once (these are mostly
  naturally idempotent).

### Channel ingress — `POST /v1/messages`

Adapter → routd. Appends one inbound `messages` row (from
`api/api.go handleMessage`). Auth: channel service token; `jid` prefix
must be owned by the calling adapter.

```jsonc
// POST /v1/messages   Authorization: Bearer <adapter service token>   X-Idempotency-Key: <id>
{
  "id": "wamid.X", // optional; routd mints <adapter>-<rand> if empty
  "chat_jid": "whatsapp:123@s.whatsapp.net",
  "sender": "123@s.whatsapp.net",
  "sender_name": "Alice",
  "content": "hi",
  "timestamp": 1716998400, // unix seconds; 0/absent → now
  "reply_to": "wamid.Y", // optional
  "reply_to_text": "...",
  "reply_to_sender": "...",
  "topic": "", // optional; inherited from reply_to parent for reactions (5/G)
  "verb": "message", // default; like|dislike|edit|delete|...
  "reaction": "👍", // optional emoji for verb=like
  "is_group": true,
  "chat_name": "#general",
  "attachments": [
    {
      "mime": "image/jpeg",
      "filename": "a.jpg",
      "url": "...",
      "data": "<b64>",
    },
  ],
  // whapd flat-attachment compatibility:
  "attachment": "<b64>",
  "attachment_mime": "image/jpeg",
  "attachment_name": "a.jpg",
}
// 200  {"ok": true, "id": "wamid.X"}     (id echoes the stored row)
// 400  {"error":"missing_field","message":"chat_jid and content required"}
// 400  {"error":"jid_prefix_mismatch","message":"jid not owned by this adapter"}
// 401  {"error":"unauthorized","message":"..."}
```

On append routd performs the ingress work inline, in order, in one tx:
reaction topic-inheritance (look up `reply_to` parent topic), 5/L
reply-to-bot → `verb=mention` promotion, `SetEngagement` for
`verb=mention` (before the row write), then `PutMessage`,
`SetChatIsGroup`, and enqueue the chat.

### Outbound passthrough — `POST /v1/outbound`

Daemon → routd → adapter, for non-agent senders (timed, onbod). Resolves
the delivering adapter and forwards. Does **not** append a `messages` row
(the caller owns its row); it is the egress proxy.

```jsonc
// POST /v1/outbound   Authorization: Bearer <service token>
{ "jid": "slack:T/C/U", "text": "scheduled note", "channel": "slakd" } // channel optional
// 200  {"ok": true}
// 404  {"error":"no_channel","message":"no channel for jid"}
// 502  {"error":"delivery_failed","message":"..."}
```

### Route CRUD — `/v1/routes`

```jsonc
GET    /v1/routes                       → 200 [ {id, seq, match, target, observe_window_messages, observe_window_chars} ]
GET    /v1/routes/{id}                  → 200 {route} | 404 {"error":"not_found"}
PUT    /v1/routes                       // replace whole table (set_routes); body: [ {seq, match, target, ...} ] → 200 {"count": N}
POST   /v1/routes                       // append one (add_route); body: {seq, match, target, ...} → 201 {route}
DELETE /v1/routes/{id}                  → 204 | 404
// 403 {"error":"forbidden","message":"routes:write at scope ⊇ target folder required"}
```

Authz: `routes:write` (operator) or `routes:write:own_group` (agent — the
`target` folder must be within the caller's folder subtree, per
`authzStructural`).

### Turn / conversation commands — `/v1/turns/{turn_id}/*`

The conversation tools the agent calls, served in-process by routd's MCP
socket — § MCP tool face (these `/v1/*` paths are the REST twin). Each
appends a `messages` row and fans out to the delivering
adapter. `turn_id` binds the command to the in-flight run (the triggering
inbound's message id); routd uses it to seed threading (`SetLastReply`)
and engagement. **`X-Idempotency-Key` required.** Auth: agent capability
token, folder-scoped (§ Auth).

```jsonc
// POST /v1/turns/{turn_id}/reply      // THE default response — threads to the conversation
{ "jid": "slack:T/C/U", "text": "answer", "reply_to_id": "" }   // optional; defaults to last_reply_id under active topic
// 200  {"message_id": "out-...", "platform_id": "1716998400.0042", "status": "sent"|"pending"}

// POST /v1/turns/{turn_id}/send       // fresh top-level message, NOT threaded
{ "jid": "slack:T/C/U", "text": "proactive note" }
// 200  {"message_id": "...", "platform_id": "...", "status": "..."}

// POST /v1/turns/{turn_id}/document   // file delivery (caption replaces a send)
{ "jid": "...", "path": "/srv/.../report.pdf", "name": "report.pdf", "caption": "see attached", "reply_to_id": "" }
// 200  {"message_id": "...", "status": "..."}

// GET  /v1/turns/{turn_id}/history?jid=...&before=<rfc3339>&limit=50&q=...
//   chronological messages on the chat from routd.db, FTS-searchable via &q=
// 200  {"source": "cache"|"platform"|"cache-only", "messages": [ {id, sender, content, timestamp, reply_to_id, is_from_me, is_bot_message} ], "cap": <int>}

// GET  /v1/turns/{turn_id}/thread?reply_to=<msg_id>&limit=50   // the reply-chain rooted at reply_to
// 200  {"messages": [ ... ]}

// POST /v1/turns/{turn_id}/like       { "jid": "...", "platform_id": "1716...0042", "reaction": "👍" }   → 200 {"ok": true}
// POST /v1/turns/{turn_id}/edit       { "jid": "...", "platform_id": "...", "content": "fixed" }          → 200 {"ok": true}
// POST /v1/turns/{turn_id}/delete     { "jid": "...", "platform_id": "..." }                              → 200 {"ok": true}
// POST /v1/turns/{turn_id}/pin        { "jid": "...", "platform_id": "..." }                              → 200 {"ok": true}
// POST /v1/turns/{turn_id}/unpin      { "jid": "...", "platform_id": "...", "all": false }                → 200 {"ok": true}
// 422 {"error":"unsupported","message":"channel does not support pin"}   // chanlib.ErrUnsupported maps here
```

`reply`/`send`/`document` follow the **append-then-deliver** contract:
write the bot row `status='pending'` (sender = the group folder,
`is_bot_message=1`, `turn_id` set), attempt delivery, mark `sent` +
`platform_id` on success or leave `pending` for the retry loop. They
write `SetLastReply` (always) and `BumpEngagement` (unless the active
turn's trigger is `timed-*`, read from `currentTrigger`). `like`/`edit`/
`delete`/`pin`/`unpin` act on a platform message by `platform_id` and do
**not** append a `messages` row (they mutate an existing platform message
via the adapter's `Socializer`).

**Adapter egress contract.** Delivery (initial + retry) calls the owning
adapter's `POST /v1/send` with
`{jid, text, reply_to_id, thread_id, idempotency_key}` where
`idempotency_key` is the bot row's stable `message_id`; the adapter
dedups platform-side and returns `{platform_id, ok}`. routd writes
`platform_id` + `status='sent'` on the `200`. `document` passes
`{jid, path, name, caption, reply_to_id, idempotency_key}`; the file at
`path` lives on the shared group volume both routd and the adapter mount,
so routd passes the path (no byte streaming). If the file is gone at
retry the adapter returns `404`, routd marks the row `failed` (no
infinite retry). This is the same `POST /v1/send` the `POST /v1/outbound`
passthrough uses; the only difference is the turn path appends the row
first.

**Batch per turn.** The agent's per-turn output is delivered as a batch
through these calls within one run; `submit_turn` (§ turn lifecycle)
closes the turn. routd sequences appends in call order so the platform
sees frames in order. Multiple `reply`s chain their `reply_to_id` to the
prior `platform_id`.

### Turn lifecycle — `submit_turn` + `turn_results`

The agent calls `submit_turn` once at the end of a run (a hidden JSON-RPC
method, not in `tools/list`, served + handled in-process by routd). routd
records the outcome idempotently:

```jsonc
// submit_turn (MCP) / POST /v1/turns/{turn_id}/result (REST twin)
{ "turn_id": "wamid.X", "session_id": "uuid", "status": "success"|"error",
  "result": "<final text, optional>", "error": "<optional>",
  "caller_sub": "u_abc", "models": { "claude-…": {"input":1200,"output":340,"cost_cents":2} } }
// 200  {"recorded": true|false}   // false = duplicate (folder,turn_id), ignored
```

`RecordTurnResult(folder, turn_id, session_id, status)` inserts into
`turn_results` (PK `(folder, turn_id)`); a duplicate returns
`recorded:false`. On a first record routd looks up `turn_context[turn_id]`
to recover `(folder, topic, chat_jid)` (the payload carries no `topic`),
persists the new `session_id` into `sessions(folder, topic)`, writes one
`cost_log` row per model from the payload's `models` map (§ cost_log —
routd persists the cost runed reports; runed never writes cost),
publishes `round_done` to the web SSE channel (keyed on the chat JID's
folder via `turn_context.chat_jid`, **not** the routing-target folder —
the known routed-web-submission gotcha), and delivers `result` if present.

**Completion reconciliation (state machine, PINNED).** Two terminal
signals exist: the `POST /v1/runs` HTTP response (`outcome` +
`session_id`) and `submit_turn`. Reconcile:

| Order of arrival                                      | Resolution                                                                                                                                                   |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `submit_turn` then run-response                       | `submit_turn` authoritative for `session_id`/`status`; run-response only marks `turn_context.done`                                                           |
| run-response `ok`/`silent`, `submit_turn` never       | run-response's `session_id` persists; cursor advances; turn closed                                                                                           |
| run-response `error`, then late `submit_turn:success` | FIRST terminal signal wins for cursor + result delivery; the late `submit_turn` records into `turn_results` (PK dedup) but does NOT re-deliver or re-advance |
| neither (crash)                                       | `turn_context.state` stays `running`; on restart the chat is re-fed from the un-advanced cursor (re-attempt; `turn_results` dedups)                          |

`agent_cursor` advances **once**, at the FIRST terminal signal, gated on
per-folder serialization so no second turn for the folder starts until
the first closes. `session_id` persistence prefers `submit_turn`'s value,
falls back to the run-response's. `turn_context.state` flips to `done` at
the first terminal signal; a duplicate `POST /v1/runs` start for an
already-`done` turn_id → `409 {"error":"turn_done"}`.

**Post-terminal callbacks** (`/v1/turns/{turn_id}/*`) remain valid until
the `POST /v1/runs` request returns, even after an early `submit_turn`
flipped the turn to `done` — the run is still live and may emit trailing
frames, and per-folder serialization guarantees no second turn started.
After the run-response returns, a late callback for that `turn_id` →
`409 {"error":"turn_done"}`.

**Crash recovery.** On startup routd re-feeds chats whose
`turn_context.state='running'` from the un-advanced `agent_cursor`
(`recoverPendingMessages`); runed containers are per-turn and exit on
their own. A re-dispatch reuses the same `turn_id` for the same trigger,
so `turn_results` PK dedups any `submit_turn` the old run delivers and the
`409 turn_done` guard blocks a double live run.

Stale `running` rows older than the run timeout are swept by the hourly GC
to a **distinct** terminal state `'expired'`, **not** `'done'`. This is
load-bearing: crash-recovery keys re-feed on `state='running'` (sweeping
to `'done'` would silently kill replay for a turn that never completed),
while the double-live-run `409 turn_done` guard keys on `state='done'`
(sweeping to `'done'` would falsely 409 a legitimate re-dispatch). An
`'expired'` row is neither re-fed (no longer `running`) nor a `done`-guard
hit — it is a swept-stale marker, preserving the at-least-once replay
semantics and the done-guard independently.

### Web routes — `/v1/web_routes`

```jsonc
GET    /v1/web_routes?folder=...   → 200 [ {path_prefix, access, redirect_to, folder, created_at} ]
PUT    /v1/web_routes              { "path_prefix":"/pub/acme/", "access":"public", "redirect_to":"", "folder":"acme" } → 200 {"ok":true}
DELETE /v1/web_routes              { "path_prefix":"/pub/acme/", "folder":"acme" } → 200 {"deleted": true|false}
// access ∈ public|auth|deny|redirect
```

### Route tokens — `/v1/route_tokens` (spec 5/W)

routd-owned bearer tokens for the `/chat/`,`/hook/` surfaces. Issue
returns the raw token **once**.

```jsonc
POST   /v1/route_tokens/chat       { "owner_folder":"acme", "target_folder":"acme", "jid_suffix":"" }
                                   → 201 {"token":"<raw>", "url":"https://…/chat/<raw>/", "jid":"web:acme", "owner_folder":"acme", "created_at":"..."}
POST   /v1/route_tokens/hook       { "owner_folder":"acme/eng", "target_folder":"acme/eng", "source_label":"github", "jid_suffix":"" }
                                   → 201 {"token":"<raw>", "url":"https://…/hook/<raw>", "jid":"hook:acme/eng/github", ...}
GET    /v1/route_tokens?owner_folder=acme  → 200 [ {jid, owner_folder, created_at} ]   // never returns raw token
DELETE /v1/route_tokens/{jid}      ?owner_folder=acme   → 204 | 404
POST   /v1/route_tokens/resolve    { "token":"<raw>" } → 200 {"jid", "owner_folder"} | 404   // webd's token→jid resolve (service-token auth)
// 403 {"error":"forbidden","message":"mint scope by tier (spec 5/W)"}
```

`owner_folder` bounds revocation: from session context for the MCP face
(never a parameter there), explicit on REST and gated via
`Authorize(principal, admin, owner_folder)`. Validation (pinned):
`target_folder` MUST equal or be a descendant of `owner_folder`;
`source_label` and `jid_suffix` MUST match `[\w-]+` per segment (they
become JID path segments — reject `/`, whitespace, `:`). Multiple active
tokens per `jid` are **permitted** (PK is `token_hash`) — a second mint is
a distinct token, never an error; revocation by `jid` deletes all tokens
for that JID under the caller's `owner_folder`.

The bearer-token URL surfaces (`GET/POST /chat/<token>/`,
`POST /hook/<token>`) live in **webd**, which does **not** own
`route_tokens` and does **not** open `routd.db` (cross-daemon direct DB
reads are barred by the DB-ownership rule). It resolves the URL token via
a dedicated routd endpoint:

```jsonc
POST /v1/route_tokens/resolve   Authorization: Bearer <webd service token>
{ "token": "<raw URL token>" }
// 200  {"jid":"web:acme", "owner_folder":"acme"}   // routd hashes sha256(token), looks up route_tokens
// 404  {"error":"unknown_token"}
```

webd then appends the request body via `POST /v1/messages` under the
returned `jid`. No ACL there; the route token IS the auth (+ webd's
per-token rate limit). routd does the `sha256(token)` hashing and the
table lookup; webd never sees the hash or the table.

## MCP tool face

routd is **agent-first**: MCP is canonical, REST is the impedance match.
Every `/v1/*` handler has an MCP twin where an agent needs it — one
handler, two faces ([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)).
The conversation tools are **served to the agent by routd's in-process
MCP socket** (`ServeTurnMCP` binds the per-folder unix socket and wires
the tools to routd's own DB + Deliverer); routd is the **handler
owner**. The same process also serves the routing-control tools. (The
"via runed's federation" framing in the table below is the descoped
design — see Status note.)

| MCP tool                                                                         | Face of                                | Served by          | Scope                        |
| -------------------------------------------------------------------------------- | -------------------------------------- | ------------------ | ---------------------------- |
| `reply`                                                                          | `POST /v1/turns/{id}/reply`            | routd (in-process) | `messages:send:own_group`    |
| `send`                                                                           | `POST /v1/turns/{id}/send`             | routd (in-process) | `messages:send:own_group`    |
| `send_file`                                                                      | `POST /v1/turns/{id}/document`         | routd (in-process) | `messages:send:own_group`    |
| `get_history` / `get_thread`                                                     | `GET /v1/turns/{id}/history`,`/thread` | routd (in-process) | `chats:read:own_group`       |
| `like` / `edit` / `delete`                                                       | `POST /v1/turns/{id}/{verb}`           | routd (in-process) | `messages:send:own_group`    |
| `dislike` / `pin_message` / `unpin_message` / `unpin_all`                        | mapped paths (§ verb→path exceptions)  | routd (in-process) | `messages:send:own_group`    |
| `post` / `forward` / `quote` / `repost`                                          | `POST /v1/turns/{id}/{verb}`           | routd (in-process) | `messages:send:own_group`    |
| `send_voice`                                                                     | `POST /v1/turns/{id}/send_voice`       | routd (in-process) | `messages:send:own_group`    |
| `engage` / `disengage`                                                           | engagement write (5/G)                 | routd (in-process) | self/owned jid (3-arm authz) |
| `fork_topic`                                                                     | lineage write (5/F)                    | routd (in-process) | `messages:send:own_group`    |
| `set_routes` / `add_route` / `delete_route`                                      | route CRUD                             | routd (in-process) | `routes:write:own_group`     |
| `inspect_routing`                                                                | route-table introspection              | routd (in-process) | `routes:read:own_group`      |
| `issue_chat_link` / `issue_webhook` / `list_route_tokens` / `revoke_route_token` | `/v1/route_tokens`                     | routd (in-process) | tier-scoped (5/W)            |

`reply`/`send` keep distinct names + sharp descriptions (threaded answer
vs. fresh top-level message), not one tool with a `mode=` param, per the
project tool-naming rule. The handler is routd's, served in-process.

**verb→path exceptions (PINNED).** Most message tools map
tool-name → `/v1/turns/{id}/<name>` directly (`reply`→`/reply`,
`send`→`/send`, `get_history`→`/history`, `get_thread`→`/thread`,
`like`→`/like`, `edit`→`/edit`, `delete`→`/delete`). Four tools do
**not** — the tool name is not the path tail (routd's in-process ipc tool
layer special-cases them):

- `send_file` → `/document`.
- `dislike` → `/like` with `reaction="👎"`. There is **no** `/dislike`
  endpoint (the dislike-via-like-emoji rule: one code path per mechanism,
  both verbs visible to the agent).
- `pin_message` → `/pin`, `unpin_message` → `/unpin` (strip `_message`;
  the bare verb tail is routd's interface — sending `/pin_message` 404s).
- `unpin_all` → `/unpin` with `all:true` (the `/v1/turns/{id}/unpin` body
  carries the `all` flag; the `_all` tool name has no separate path).

## The routd↔runed interface (PINNED)

routd drives the agent run; runed runs it. The contract is pinned —
runed's spec is written to match exactly.

**routd → runed: start a run.**

```jsonc
// POST <RUNED_URL>/v1/runs   Authorization: Bearer <routd service token>
{
  "folder": "acme/eng",
  "topic": "deploy",
  "chat_jid": "slack:T/C/U",
  "session_id": "uuid-or-empty",        // empty = fresh session; runed resumes if non-empty
  "message_batch": "<rendered prompt: sysMsgs+autocalls+persona+<observed>+feed>",
  "trigger_sender": "u_abc",            // for engagement-skip policy
  "caller_sub": "user:u_abc",           // token SUBJECT for the brokered agent token; "service:routd" for daemon-triggered; never ""
  "turn_id": "wamid.X",                 // the triggering inbound id; echoed on the conversation callbacks
  "capability_scopes": ["messages:send:own_group", "chats:read:own_group", "..."],
  "model": "",                          // group override; empty = instance default
  "container_config": { /* opaque GroupConfig forwarded from groups.container_config */ },
  "isolated": false                     // timed-isolated:* runs get a one-off container, no session persist
}
// 200 (sync, run complete)
{ "run_id": "run-…", "outcome": "ok"|"error"|"silent", "session_id": "uuid", "error": "",
  "steered": false,        // discriminator: false = turn-boundary outcome; true = steer ack (P-runed § steer)
  "breaker_open": false }  // true ONLY on the run that trips runed's circuit breaker (P-runed § queue)
// 503 {"error":"queue_shutting_down"}
```

- **Sync, frames out-of-band.** The call is **synchronous for the turn
  boundary**: routd blocks on the HTTP response, which returns when the
  run completes (mirrors today's `runner.Run` return). The agent's
  conversation frames arrive **out-of-band during the run** via the
  callbacks below, not in this response body. `submit_turn` is the
  canonical end-of-turn signal; the response carries `outcome` +
  `session_id` as a **backstop** if the agent never called `submit_turn`
  (e.g. crash). `submit_turn`'s `session_id`/`error` wins over the
  response body when both arrive.
- **`outcome`**: `ok` (ran, may or may not have replied — advance cursor,
  record), `error` (run failed — advance past batch, mark rows errored,
  send failure notice), `silent` (ran, no deliverable output — logged, no
  error).

**Transport semantics (PINNED).**

- **Timeout.** routd applies hard deadline `RUNED_RUN_TIMEOUT` (default =
  group container timeout + grace). On deadline routd cancels the HTTP
  request; runed `Stop`s the container (`ContainerRuntime.Stop`
  graceful-then-kill) on context cancel. A timed-out run is `outcome:error`
  for cursor purposes (advance past batch — the starvation guard).
- **Cancel.** Request-scoped only: routd cancels by dropping the request;
  no `DELETE /v1/runs/{run_id}`. runed reaps on context cancel.
- **`run_id`** is runed-minted, unique per call, for log correlation only.
  Run idempotency is keyed on `turn_id` (`turn_context` PK +
  `409 turn_done`), NOT `run_id`.
- **Transport failure vs `outcome:error`.** A transport failure (TCP
  reset, 5xx, timeout) is **distinct** from a clean `outcome:error`: on
  transport failure routd doesn't know whether the run happened, so it
  does NOT advance the cursor and the chat is re-fed next poll
  (at-least-once; `turn_results` dedups a redundant `submit_turn`). A clean
  `200 {outcome:error}` means the run definitively failed — advance past
  the batch (no infinite replay). `POST /v1/runs` is **NOT** blindly
  retried; re-attempt is the normal poll re-feed, gated by per-folder
  serialization.

**The agent's conversation tools (REST twin).** The agent's
`reply`/`send`/`get_history`/… tools are served by routd's in-process MCP
socket; their REST twins are the `/v1/turns/{turn_id}/*` handlers (above).
routd is the sole appender; runed never writes a `messages` row. The
`turn_id` binds each call to the run routd started. (As specced these were
HTTP callbacks from runed's federation; the flip serves them in-process —
see Status note. The wire shapes still hold for the REST twin.) The full
`/v1/turns/{turn_id}/*` surface:

| `/v1/turns/{turn_id}/*`               | When                  | Auth (every call)               |
| ------------------------------------- | --------------------- | ------------------------------- |
| `POST /v1/turns/{turn_id}/reply`      | agent `reply`         | agent capability token (Bearer) |
| `POST /v1/turns/{turn_id}/send`       | agent `send`          | agent capability token          |
| `POST /v1/turns/{turn_id}/document`   | agent `send_file`     | agent capability token          |
| `GET  /v1/turns/{turn_id}/history`    | agent `get_history`   | agent capability token          |
| `GET  /v1/turns/{turn_id}/thread`     | agent `get_thread`    | agent capability token          |
| `POST /v1/turns/{turn_id}/{verb}`     | like/edit/delete/pin… | agent capability token          |
| `POST /v1/turns/{turn_id}/post`       | agent `post`          | agent capability token          |
| `POST /v1/turns/{turn_id}/forward`    | agent `forward`       | agent capability token          |
| `POST /v1/turns/{turn_id}/quote`      | agent `quote`         | agent capability token          |
| `POST /v1/turns/{turn_id}/repost`     | agent `repost`        | agent capability token          |
| `POST /v1/turns/{turn_id}/send_voice` | agent `send_voice`    | agent capability token          |
| `POST /v1/turns/{turn_id}/result`     | agent `submit_turn`   | agent capability token          |

The last row is the **turn outcome**: the agent's `submit_turn` is the
in-process MCP twin of `POST /v1/turns/{turn_id}/result` (§ turn
lifecycle), carrying the `TurnResult` payload (`session_id`, `status`,
optional `result`/`error`; the **cost** breakdown is reported on runed's
`POST /v1/runs` response — routd persists it, § cost_log). routd records
the result idempotently into `turn_results` (PK `(folder, turn_id)`); a
duplicate returns `{recorded:false}`. The agent capability token is
offline-verified + scope-checked by routd; runed never re-signs.

**Invariants.** routd never spawns a container or holds a Docker handle.
runed never opens `routd.db` or appends a message. The agent run is
driven over the `POST /v1/runs` contract; the conversation tools are
served in-process by routd's MCP socket. The `ContainerRuntime` seam
([`U-genericization.md`](U-genericization.md) § ContainerRuntime) lives
entirely inside runed.

## Auth

routd is a **verifier, not a signer**. It holds no signing key; it
offline-verifies tokens against authd's cached JWKs via the `auth/`
library ([`1-auth-standalone.md`](1-auth-standalone.md)). Two credential
classes cross routd's boundary — keep them separate:

| Credential             | Issued by | Verified by routd how                                    | Used for                                              |
| ---------------------- | --------- | -------------------------------------------------------- | ----------------------------------------------------- |
| Agent capability token | `authd`   | `auth.VerifyHTTP` offline against cached JWKs            | `/v1/turns/*`, `/v1/routes` (agent), conversation MCP |
| Adapter/service token  | `authd`   | same offline verify; `sub = service:<adapter>`           | `POST /v1/messages` ingress, `POST /v1/outbound`      |
| **Route token** (5/W)  | **routd** | `sha256(token)` lookup in `route_tokens` (routd's table) | webd's `/chat/<token>/`, `/hook/<token>` surfaces     |

A route token is a 32-byte opaque secret stored hashed; it is not a JWT,
carries no scope, and authd never sees it. It authorizes exactly "append
at this JID" at the public web surface — nothing else. An agent
capability token is an authd-minted ES256 JWT with `scope` + `arz/folder`
gating the `/v1/*` API. Verifying one is never a path to the other.

routd obtains its own `service:routd` token at boot (`auth.ServiceToken`
against authd) to authenticate daemon→daemon calls (`POST /v1/runs` to
runed, reads against runed / cost owner). The HMAC `CHANNEL_SECRET` /
`PROXYD_HMAC_SECRET` paths are gone — retired in the authd cutover that
precedes this split
([`1-auth-standalone.md`](1-auth-standalone.md) § HMAC retirement).

## Standalone-ready acceptance

One contract test (the [`U-genericization.md`](U-genericization.md)
§ Acceptance bar for routd), in `tests/standalone/routd_test.go`:

> Boots with `DB_PATH=/tmp/routd.db` and a stub `RUNED_URL`; runs its own
> migrations; accepts an inbound via `POST /v1/messages`; resolves a route
> from a single `PUT /v1/routes` rule; the loop dispatches a (stub) run by
> calling `POST <RUNED_URL>/v1/runs` and records the `submit_turn` outcome
> in `turn_results`. No `core.Folder` leak / no arizuko-domain hardcoding
> beyond `types.*` (cross-boundary signatures use `types.Folder` /
> `types.UserSub` / `types.Scope`; the wire shape may render the field as
> `tenant_id` while the Go type is `types.Folder`).

The stub runed records the `POST /v1/runs` body, replies
`{outcome:"ok", session_id:"stub"}`, then calls back
`POST /v1/turns/{turn_id}/reply` to prove the sole-appender callback.
Asserts: (1) the inbound row exists in `messages`; (2) the route resolved
to the expected folder; (3) runed was called with the rendered batch +
`turn_id`; (4) the callback reply appended exactly one bot row
`is_bot_message=1`; (5) `turn_results` has one row for `(folder, turn_id)`
and a duplicate `submit_turn` is dropped.

## Code pointers

> Extracting `gateway/` + `proxyd/` here absorbs **`5/6` 6/6b** (inbound
> `enrichAttachments`→`enrichBatch` chain, kills the two-call-site drift)
> and **6/6c** (HTTP `groupScope` factor out of `davRoute`). Factor them as
> the loop/ingress are built; `5/6` is the design reference.

- `gateway/gateway.go` — the loop (`pollOnce`, `processGroupMessages`,
  `processSenderBatch`, `resolveOrEngaged`, `handleSubmitTurn`,
  `publishRoundDone`, `issueRouteToken`). The `GatedFns`/`StoreFns` seams
  become routd's `/v1/*` + MCP handlers; `runAgentWithOpts` becomes the
  `POST /v1/runs` call to runed.
- `api/api.go` — `handleMessage` (ingress), `handleOutbound`, route-token
  REST handlers, the 5/L promotion + 5/G engagement-on-mention block.
- `router/router.go` + `core.ParseRouteTarget` — route resolution,
  `RouteMatches`, `#observe`/`#topic` fragment parsing, formatters.
- `store/` — every table above; the access methods become routd's DB
  layer.
- `ipc/ipc.go` — the conversation + routing MCP tool definitions.
- `runed`'s spec ([`P-runed.md`](P-runed.md)) — the `POST /v1/runs` peer.

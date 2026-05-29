---
status: partial
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

**Decided** (operator + oracle, 2026-05-29). `routd` is the daemon
carved out of `gated` that owns **routing rules + the message/event
store + the orchestration loop + channel ingress/egress**. It is the
**sole appender of messages** — every inbound from an adapter, every
outbound from an agent, every delegation hop, lands as a `routd`-owned
`messages` row, written by `routd` and nobody else. The atomic unit is
the chat: "append inbound message → resolve route → start/continue
turn" share one transactional view per `(chat, group)`.

This spec is **build-ready**: the `routd.db` schema, the `/v1/*`
surface, the orchestration-loop algorithm, the `routd↔runed`
interface, the MCP tool face, the auth posture, and the
standalone-acceptance test below are concrete enough to implement
`routd` without further design decisions. `status: partial` reflects
that the code is not yet built — the loop lives today inside
`gateway/gateway.go` + `api/api.go` + `store/` + `router/`, which this
spec extracts verbatim into a standalone daemon.

`routd` is part of the **second** gated-split release — the
`routd` + `runed` big-bang multi-DB cutover, after `authd`
ships standalone first ([`U-genericization.md`](U-genericization.md)
§ Phase C, § HMAC retirement). One coordinated migration: the two
daemons carve their tables into their own DBs, the monolithic
`messages.db` schema-authority in `gated` is deleted, no shared-DB
interim, no backward-compat shim ([`U-genericization.md`](U-genericization.md)
NO BACKWARD COMPATIBILITY). The agent MCP socket, tool federation, and
capability-token brokering live in `runed` ([`P-runed.md`](P-runed.md)) —
there is no separate MCP-host daemon.

## What routd owns vs. what it doesn't

`routd` is the conversation engine. The three other split products
keep their lanes:

| Concern                                                    | Owner                                |
| ---------------------------------------------------------- | ------------------------------------ |
| Routing rules, message/event store, orchestration loop     | `routd`                              |
| Channel ingress (`POST /v1/messages`) + egress (delivery)  | `routd`                              |
| Token signing, JWKs, OAuth login                           | `authd`                              |
| Container lifecycle, per-spawn state, the agent run itself | `runed`                              |
| Per-tenant MCP socket, tool federation, capability tokens  | `runed` ([`P-runed.md`](P-runed.md)) |

Hard boundaries, no overlap:

- **routd never spawns containers.** It calls `runed` over HTTP
  (§ The routd↔runed interface) and never touches Docker.
- **runed never appends messages.** The agent's `reply`/`send`/`edit`
  tools (hosted by `runed`'s MCP host / federation, executed inside the
  container) call **back** into routd's `/v1/turns/{turn_id}/*` — routd
  is the sole appender.
- **routd is a token verifier, not a signer.** It offline-verifies
  agent capability tokens against authd's JWKs (§ Auth). Route-tokens
  (the `/chat/`,`/hook/` widget tokens) are a **distinct** credential
  routd owns outright.
- **routd hosts no agent MCP socket.** The unix socket per folder is
  `runed`'s; routd's MCP face is routing-control
  tools served over routd's own surface, plus the conversation-command
  handlers that `runed`'s MCP host federates (one handler, two faces —
  § MCP tool face).

## routd.db schema

`routd` owns `routd.db` — its own SQLite file (WAL), its own
`routd/migrations/` subdir, per the
[`U-genericization.md`](U-genericization.md) DB-ownership rule (each
daemon owns its DB + migrations; no daemon migrates another's schema).
The tables below carry the **live column shapes** out of today's
`messages.db` (`store/migrations/*.sql` — the latest rebuild of each
table is canonical). Times are RFC3339Nano UTC TEXT throughout
(matches today's store convention; the Go layer computes every
timestamp — no `strftime` in SQL, which would diverge the format).

The cutover copies the listed tables' rows out of `messages.db` into
`routd.db` then drops them from the source, one-shot, no dual-write
period.

### Group identity — `groups`

The routing/group identity table. (Renamed from the historical
`registered_groups`, which was dropped in migration `0020`; path is
identity since `0051` — `parent` derived via `filepath.Dir(folder)`,
display via `filepath.Base(folder)`.) Carries per-group routing knobs
(visibility, observe-window caps) that the loop reads on every turn.

```sql
CREATE TABLE groups (
  folder                  TEXT PRIMARY KEY,         -- path IS identity ("krons", "atlas/support")
  added_at                TEXT NOT NULL,
  container_config        TEXT,                     -- JSON GroupConfig (mounts/timeout/max_children); opaque to routd, forwarded to runed
  updated_at              TEXT,
  product                 TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day  INTEGER NOT NULL DEFAULT 0,
  open                    INTEGER NOT NULL DEFAULT 1,  -- 1 = visible to open siblings as ambient source (spec 5/F)
  observe_window_messages INTEGER,                  -- per-group cap; NULL = inherit env default
  observe_window_chars    INTEGER,                  -- per-group cap; NULL = inherit env default
  model                   TEXT                      -- per-group model override; NULL = instance default
);
```

`container_config` and `model` are forwarded to `runed` at run time;
routd treats them as opaque payload (it does not interpret mounts or
timeouts — that is runed's concern).

### Chats — per-chat cursor + sticky state

```sql
CREATE TABLE chats (
  jid          TEXT PRIMARY KEY,     -- prefix:identifier (channel) | bare folder | web:<f> | hook:<f>/<src>
  errored      INTEGER NOT NULL DEFAULT 0,
  agent_cursor TEXT,                 -- RFC3339Nano; high-water mark of messages fed to the agent
  sticky_group TEXT,                 -- @-prefix routing pin (spec 5/Q)
  sticky_topic TEXT,                 -- #-prefix topic pin
  is_group     INTEGER NOT NULL DEFAULT 0
);
```

`is_group` is set per inbound (`SetChatIsGroup`); the others form the
per-chat conversation state the loop reads/advances atomically with
turn dispatch (§ The orchestration loop).

### Messages — the single event log (sole-append)

The one table all inbound and outbound flow through. routd is its only
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
  forwarded_from  TEXT,                -- delegation/escalation return address (spec 5/Q)
  reply_to_id     TEXT,
  reply_to_text   TEXT,
  reply_to_sender TEXT,
  topic           TEXT NOT NULL DEFAULT '',
  routed_to       TEXT NOT NULL DEFAULT '',  -- folder this row was routed/delivered to
  verb            TEXT NOT NULL DEFAULT 'message',  -- message|mention|like|dislike|edit|delete|join|untrusted|...
  attachments     TEXT NOT NULL DEFAULT '',  -- JSON []InboundAttachment (cleared after enrich)
  source          TEXT NOT NULL DEFAULT '',  -- adapter that handled the row (inbound: receiver; outbound: deliverer)
  is_observed     INTEGER NOT NULL DEFAULT 0, -- stored under an observe-mode route; no turn fired (spec 5/B)
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
  match                    TEXT    NOT NULL DEFAULT '', -- space-separated key=glob predicates (§ Match grammar)
  target                   TEXT    NOT NULL,            -- folder | folder#observe | folder#<topic> (core.ParseRouteTarget)
  observe_window_messages  INTEGER,                     -- per-route cap (wins over per-group)
  observe_window_chars     INTEGER
);
CREATE INDEX idx_routes_seq ON routes(seq);
```

`match` keys: `platform`, `room`, `chat_jid`, `sender`, `verb`. Glob
is `path.Match` (`*` does not cross `/`); `key=` means "field absent";
omitting a key leaves it unconstrained (§ The orchestration loop
references `router.RouteMatches`).

### Topic lineage + sessions — `sessions`

Per-`(folder, topic)` session id + the fork lineage (spec 5/F). routd
owns the lineage columns (`parent_topic`, `forked_at`,
`observed_cursor`); the `session_id` value itself is opaque to routd —
runed produces it and routd persists it via `submit_turn`
(§ turn lifecycle).

```sql
CREATE TABLE sessions (
  group_folder    TEXT NOT NULL,
  topic           TEXT NOT NULL DEFAULT '',
  session_id      TEXT NOT NULL,
  parent_topic    TEXT,                   -- *string: distinguishes "fork from main" ("") from "no parent" (NULL)
  forked_at       TEXT,
  observed_cursor TEXT,                   -- per-topic watermark over is_observed messages
  PRIMARY KEY (group_folder, topic)
);
CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);
```

### Engagement — `chat_reply_state`

Last-reply threading anchor + the spec 5/G engagement deadline, keyed
per `(jid, topic)`.

```sql
CREATE TABLE chat_reply_state (
  jid            TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  last_reply_id  TEXT NOT NULL,          -- thread anchor for reply-by-default
  engaged_until  TEXT,                   -- RFC3339Nano deadline; NULL = idle (spec 5/G)
  engaged_folder TEXT NOT NULL DEFAULT '', -- folder claiming the engagement; resolves the routing fallback
  PRIMARY KEY (jid, topic)
);
```

### Turn context + results — `turn_context`, `turn_results`

`turn_context` binds a `turn_id` to its `(folder, topic, chat_jid)` at
run-start so the conversation callbacks (`/v1/turns/{turn_id}/*`) and
`submit_turn` can recover the active topic — the `submit_turn` payload
carries no `topic`, and the callback path knows only `turn_id`. Written
in the same tx that dispatches the run; survives restart (so a late
`submit_turn` after a routd bounce still resolves its topic).

```sql
CREATE TABLE turn_context (
  turn_id      TEXT PRIMARY KEY,         -- the triggering inbound's message id
  folder       TEXT NOT NULL,
  topic        TEXT NOT NULL DEFAULT '',
  chat_jid     TEXT NOT NULL,
  trigger_sender TEXT NOT NULL,          -- for the timed-* engagement-skip
  started_at   TEXT NOT NULL,
  run_id       TEXT,                     -- runed's run id once POST /v1/runs returns
  state        TEXT NOT NULL DEFAULT 'running'  -- running | done (set when submit_turn or run-response terminal)
);
```

`turn_results` is the idempotency ledger for agent-submitted turn
**outcomes** (§ turn lifecycle). PK `(folder, turn_id)` is the dedup
key.

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

### Web routes — `web_routes`

URL-tree access map for the operator web layer (spec 5/V). Row-shaped
(single writer); group removal cascades.

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

The `/chat/`,`/hook/` widget/webhook bearer tokens (spec 5/W). Stored
as `sha256(token)`; raw token returned once at issue. `owner_folder`
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

### Ancillary — system messages, watchers

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

-- Directional cross-folder ambient (spec 5/F observe_group): observer
-- receives source's messages as <observed> context on its next turn.
CREATE TABLE group_watchers (
  observer TEXT NOT NULL,
  source   TEXT NOT NULL,
  PRIMARY KEY (observer, source)
);

-- Idempotency ledger for append/mutation endpoints (§ Idempotency).
-- One row per (endpoint, key); stores the original response so a replay
-- returns the exact same status+body without re-executing. Survives
-- restart (it's in routd.db). Swept hourly past expires_at.
CREATE TABLE idempotency_keys (
  endpoint     TEXT NOT NULL,            -- e.g. "POST /v1/turns/reply"
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

**Migration note.** `routd` runs its own migrations from
`routd/migrations/*.sql` at startup, same numbering convention as
today's `store/migrations/`. The cutover copies the above tables'
rows from `messages.db` and drops the source tables; tables NOT listed
here (`secrets`, `acl`, `acl_membership`, `scheduled_tasks`,
`network_rules`, `cost_log`, `audit_log`, `config_meta`, `pane_sessions`,
auth tables, onboarding) belong to other split products or stay in the
residual gated and are **not** part of `routd.db`. `pane_sessions` is
read by the loop's `paneHints` but **owned by runed**; routd reads it
over `runed`'s `/v1/*`, never by direct SQL.

## The orchestration loop

The algorithm extracted verbatim from `gateway/gateway.go`
(`pollOnce` / `processGroupMessages` / `processSenderBatch`). It is
poll-driven over `routd.db`; ingress (§ POST /v1/messages) writes the
inbound row then enqueues the chat for the loop.

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
    if handleStickyCommand(chat_jid, last): continue   # @group / #topic nav (spec 5/Q)
    if handleCommand(last, group): continue            # gateway slash-commands
    if tryExternalRoute(...): advance; continue        # @child delegation / reply-chain target
    rt := router.ResolveRouteTarget(last, routes)
    effTopic := effectiveTopic(chat_jid, last.Topic)   # sticky_topic overrides
    if engaged(chat_jid, engTopic):                     # spec 5/G — engagement OVERRIDES route table
      group = EngagedFolder(chat_jid, engTopic)
    else if rt.Mode == "observe":                        # spec 5/B — silent ingest
      MarkMessagesObserved(rt.Folder, ids); advance; continue
    if rt.Topic != "": pin topic on chatMsgs            # route-pinned topic (spec 5/F)
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
    buildAgentPrompt(group, topic, batch)               # sysMsgs + autocalls + persona + <observed> + feed (advances observed_cursor)
    SetLastReply(chat, topic, last.id, folder)          # seed reply-to-bot threading
    setCurrentTurn(folder, last.sender, topic)          # publish trigger for engagement-skip + reply fallback
    turn_context.put(turn_id=last.id, folder, topic, chat_jid, trigger=last.sender)  # bind turn_id → (folder,topic,chat)
    out := dispatchRun(group, prompt, turn_id=last.id, ...)  # § The routd↔runed interface; 409 turn_done if already terminal
    on error & no output: advance cursor PAST batch; mark errored; send failure notice
  advance agent_cursor
```

### Web chats (`processWebTopics`)

`web:` chats are processed per topic, not per sender: routd groups the
chat's pending rows by `topic` (preserving first-seen order),
ensures each topic's lineage, builds one prompt per topic, and
dispatches one run per topic in topic order. Each topic run advances
`agent_cursor` for the chat to that topic-batch's tail and advances the
topic's own `observed_cursor`; topics in the same web chat do not share
a session (each `(folder, topic)` keeps its own `sessions` row). A
web-chat run is scheduled like any other folder turn — serialized under
the folder queue, after which the next chat is processed.

### Route resolution (`resolveOrEngaged`)

One renderer, two call sites (`pollOnce` + `processGroupMessages`) —
the single source of truth for "which group owns this chat":

1. **Direct address.** `web:<folder>` and bare-folder JIDs that match
   a registered group resolve to that group directly; the route table
   does **not** apply to them.
2. **Route table.** `router.ResolveRouteTarget(msg, routes)` scans
   `routes` by ascending `seq`, returns the first whose `match`
   predicates all pass; parses the `target` fragment (`core.ParseRouteTarget`
   → `{Folder, Topic, Mode}`). Priority within the table is
   match-key + `seq`: @mention rows (`verb=mention`) stack above
   `#observe` catch-alls (spec 5/B).
3. **Engagement override** (spec 5/G). When `(jid, effTopic)` is
   engaged (`engaged_until > now`), the engaged folder **overrides**
   the route table entirely — including `#observe` and routes pointing
   elsewhere. Checked before the `#observe` branch. On a route miss,
   the engagement fallback (`EngagedFolder`) still delivers the inbound
   to the last folder that spoke there; only a true idle miss drops.
4. **5/L promotion** happens at ingress, not here: an inbound whose
   `reply_to_id` points at a bot-authored message (`is_bot_message=1`)
   is promoted to `verb=mention` before the row is written, so routing
   sees one uniform trigger signal across all adapters.

### Concurrency model (PINNED)

**routd is a single process per instance.** It is NOT
multi-instance/HA — the orchestration loop, the per-folder queue, and
the in-memory turn state (`currentTrigger`, `inFlightTurns`,
`steeredTs`) are process-local, exactly as today's gateway. Running two
routd processes against one `routd.db` is unsupported (the design
choice, not a TODO). Scale is per-instance (`solo/inbox` and
`corp/eng/...` run one routd each); cross-instance scale is separate
deployments, never shared-DB replicas. This is why no DB job-claim
protocol is specified: the in-memory queue (keyed by destination folder
via `folderForJid`) is the single arbiter, and `agent_cursor` recovery
(below) handles crash restart. Removing this constraint is a future
spec, not an implementation decision.

### Atomic / ordered-per-chat consistency contract

The load-bearing invariant: **append inbound → resolve route →
start/continue turn is one ordered view per `(chat, group)`**, serial
within a folder.

- **Single appender.** Every `messages` row is written by routd. No
  other daemon writes the event log, so the cursor (`agent_cursor`)
  and the row sequence never diverge across writers.
- **Serialized per folder.** The in-memory queue runs at most one turn
  per folder at a time. Concurrent inbounds to the same folder are
  absorbed into the running turn (steered) or queued behind it. The
  queue also collapses duplicate enqueues for the same chat (a chat
  already queued/running is not double-scheduled). `currentTrigger` /
  `currentTopic` are per-folder single-turn state.
- **Per-turn callback serialization.** routd serializes the
  `/v1/turns/{turn_id}/*` append-and-deliver calls **per `turn_id`**
  (the run is single-threaded inside the container, but routd takes a
  per-`turn_id` lock so even out-of-order arrivals append in receive
  order). Reply chaining: each new `reply` chains `reply_to_id` to the
  prior reply's `platform_id`; if the prior reply is still `pending`
  (delivery not yet confirmed, no `platform_id`), the new reply chains
  to the prior **internal `message_id`** instead, and the retry loop
  rewrites the platform thread anchor when the prior row's `platform_id`
  lands. An idempotent replay of an earlier command after later ones
  succeeded returns the original stored response (no reorder) — the
  ledger row is frozen at first execution.
- **Engagement writes move with the row.** Ingress writes
  `SetEngagement` for a `verb=mention` inbound **before** `PutMessage`,
  so the row, the routing decision, and the engagement claim commit
  together (§ POST /v1/messages). At outbound, `SetLastReply` (always)
  - `BumpEngagement` (unless `timed-*` trigger) write the same row.
- **Observed-cursor advance (spec 5/B/5/F).** `sessions.observed_cursor`
  is **per-(folder, topic)**. It advances when `buildAgentPrompt`
  reads the observed window for a turn: routd computes the newest
  observed timestamp included and writes it as the new cursor in the
  **same step as prompt construction**, before the run dispatches.
  Advance is NOT transactional with the run completing — at-least-once,
  so a crash between cursor-write and run may re-show an observed
  message on the next turn (benign: the prompt rule says don't act on
  observed). Two topics in one folder each carry their own cursor, so
  neither consumes the other's ambient context.
- **Cursor recovery.** `agent_cursor` advance is **not** transactional
  with the agent run (the run happens in runed). On crash mid-turn the
  chat is re-fed from the un-advanced cursor on restart
  (`recoverPendingMessages` re-enqueues chats with pending rows). The
  `turn_results` PK `(folder, turn_id)` dedups duplicate `submit_turn`
  callbacks so a replayed turn records once.
- **Outbound is poll-reconciled.** A bot row is written `status=
'pending'`, delivery attempted inline, marked `sent` on success;
  the `outboundRetryLoop` re-dispatches `pending` rows older than 30s
  and fails them after 24h. **Outbound dedup is the adapter's
  contract**: routd passes the bot row's stable `message_id` as the
  delivery idempotency key (`turn_id` + sequence) on every (re)send via
  the adapter `/v1/*`, and the adapter dedups platform-side on that key
  — a redelivered `pending` row must not create a second platform
  message. Adapters that can't dedup risk a duplicate on retry; the
  30s/24h windows bound the exposure. Delivery never blocks the turn.

## api/v1 surface

`routd/api/v1/` is the published-contract package (wire types + thin
client), importing only `types/`
([`U-genericization.md`](U-genericization.md) § Per-service api/v1).
Every endpoint has an MCP twin where an agent needs it (§ MCP tool
face) — one hand-written handler, two faces
([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)).

All JSON errors use `{"error":"<code>","message":"<human>"}` with the
HTTP status carrying the class. `GET /openapi.json` and `GET /health`
are public; everything else is auth-gated (§ Auth).

**Idempotency (mandatory).** Every append/mutation endpoint accepts an
`X-Idempotency-Key` header. The dedup ledger is the `idempotency_keys`
table (§ schema) — persistent, so dedup survives a routd restart. The
protocol, pinned:

1. On request, compute `request_hash = sha256(canonical-body)` and try
   `INSERT` into `idempotency_keys(endpoint, key, request_hash, ...)`
   with a placeholder status. The first writer wins the row (the PK
   `(endpoint, key)` serializes concurrent retries). **Canonical body**
   = the raw request bytes after JSON re-marshal with sorted object
   keys and no insignificant whitespace (Go: decode to
   `map[string]any` → `json.Marshal`, which sorts keys), so encoder
   differences don't produce false 409s or misses.
2. **Insert won** → execute the handler, then `UPDATE` the row with the
   real `(status, response)` in the **same tx** that appends the
   `messages` row. The committed row IS the durable record; if the
   process crashes after commit but before replying, the retry sees the
   committed row and replays it (case "first attempt committed but
   failed before returning").
3. **Insert lost** (row exists) → if its `request_hash` matches, replay
   the stored `(status, response)` verbatim without re-executing; if it
   **differs**, return `409 {"error":"idempotency_key_reuse"}` (same
   key, different body is a client bug, never a silent overwrite).
4. Rows expire 24h after `created_at`; an hourly GC sweep drops expired
   rows. A key reused after expiry is a fresh request.

Per-endpoint key rules (no ambiguity):

- **`POST /v1/messages`**: the dedup key is the **message `id`** (the
  `messages` PK), independent of `X-Idempotency-Key`. A duplicate `id`
  is a no-op insert that returns `200` echoing the stored row — even if
  the body differs, the **first-written row is authoritative** (the
  platform message id is immutable; a re-delivery with mutated fields is
  the adapter's bug, and silently keeping the first is correct for an
  append-only log). `X-Idempotency-Key` is honored only when `id` is
  **absent** (adapters that don't mint stable ids); routd then mints
  `id = <adapter>-<idempotency-key>` so the two keys collapse to one.
  Sending both a stable `id` and a different `X-Idempotency-Key` is
  rejected `400 {"error":"ambiguous_idempotency"}`.
- **Turn commands** (`reply`/`send`/`document`): `X-Idempotency-Key` is
  **required**. The stored `response` is the original
  `{message_id, platform_id, status}`; a replay returns it verbatim and
  does **not** append a second row or re-deliver. This is how
  command-level dedup is reconstructed — the ledger row, not
  `turn_results` (which dedups only `submit_turn`).
- **Mutations** (`like`/`edit`/`delete`/`pin`/`unpin`, route CRUD,
  web-route, route-token): `X-Idempotency-Key` optional but honored via
  the same ledger; a missing key means the caller accepts at-least-once
  (these are mostly naturally idempotent — `edit` to the same content,
  `delete` of a gone message).

### Channel ingress — `POST /v1/messages`

Adapter → routd. Appends one inbound `messages` row. (Extracted from
`api/api.go handleMessage`.) Auth: channel service token (§ Auth);
`jid` prefix must be owned by the calling adapter.

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
  "topic": "", // optional; inherited from reply_to parent for reactions (spec 5/G)
  "verb": "message", // default "message"; like|dislike|edit|delete|...
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

On append routd performs the ingress-side work inline, in order, in
one tx: reaction topic-inheritance (look up `reply_to` parent topic),
5/L reply-to-bot → `verb=mention` promotion, `SetEngagement` for
`verb=mention` (before the row write), then `PutMessage`,
`SetChatIsGroup`, and enqueue the chat for the loop.

### Outbound passthrough — `POST /v1/outbound`

Daemon → routd → adapter, for non-agent senders (timed, onbod). Resolves
the delivering adapter and forwards. Does **not** append a `messages`
row by itself (the caller owns its row); it is the egress proxy.

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
PUT    /v1/routes                       // replace the whole table (set_routes); body: [ {seq, match, target, ...} ]
                                        → 200 {"count": N}
POST   /v1/routes                       // append one (add_route); body: {seq, match, target, ...}
                                        → 201 {route} (with assigned id)
DELETE /v1/routes/{id}                  → 204 | 404
// 403 {"error":"forbidden","message":"routes:write at scope ⊇ target folder required"}
```

Authz: `routes:write` (operator) or `routes:write:own_group` (agent,
self/descendant — the `target` folder must be within the caller's
folder subtree, per `authzStructural` today).

### Turn / conversation commands — `/v1/turns/{turn_id}/*`

The conversation tools the agent calls (through `runed` federation —
§ MCP tool face). Each appends a `messages` row and fans out to the
delivering adapter. `turn_id` binds the command to the in-flight run
(it is the triggering inbound's message id); routd uses it to seed
threading (`SetLastReply`) and engagement. **`X-Idempotency-Key`
required.** Auth: agent capability token, folder-scoped (§ Auth).

```jsonc
// POST /v1/turns/{turn_id}/reply      // THE default response — threads to the conversation
{ "jid": "slack:T/C/U", "text": "answer", "reply_to_id": "" }   // reply_to_id optional; defaults to last_reply_id under active topic
// 200  {"message_id": "out-...", "platform_id": "1716998400.0042", "status": "sent"|"pending"}

// POST /v1/turns/{turn_id}/send       // fresh top-level message, NOT threaded
{ "jid": "slack:T/C/U", "text": "proactive note" }
// 200  {"message_id": "...", "platform_id": "...", "status": "..."}

// POST /v1/turns/{turn_id}/document   // file delivery (caption replaces a send)
{ "jid": "...", "path": "/srv/.../report.pdf", "name": "report.pdf", "caption": "see attached", "reply_to_id": "" }
// 200  {"message_id": "...", "status": "..."}

// GET  /v1/turns/{turn_id}/history?jid=...&before=<rfc3339>&limit=50
//   chronological messages on the chat from routd.db, FTS-searchable via &q=
// 200  {"source": "cache"|"platform"|"cache-only", "messages": [ {id, sender, content, timestamp, reply_to_id, is_from_me, is_bot_message} ], "cap": <int>}

// GET  /v1/turns/{turn_id}/thread?reply_to=<msg_id>&limit=50
//   the reply-chain rooted at reply_to
// 200  {"messages": [ ... ]}

// POST /v1/turns/{turn_id}/like       { "jid": "...", "platform_id": "1716...0042", "reaction": "👍" }   → 200 {"ok": true}
// POST /v1/turns/{turn_id}/edit       { "jid": "...", "platform_id": "...", "content": "fixed" }          → 200 {"ok": true}
// POST /v1/turns/{turn_id}/delete     { "jid": "...", "platform_id": "..." }                              → 200 {"ok": true}
// POST /v1/turns/{turn_id}/pin        { "jid": "...", "platform_id": "..." }                              → 200 {"ok": true}
// POST /v1/turns/{turn_id}/unpin      { "jid": "...", "platform_id": "...", "all": false }                → 200 {"ok": true}
// 422 {"error":"unsupported","message":"channel does not support pin"}   // chanlib.ErrUnsupported maps here
```

`reply`/`send`/`document` follow the append-then-deliver contract:
write the bot row `status='pending'` (sender = the group folder,
`is_bot_message=1`, `turn_id` set), attempt delivery, mark `sent` +
`platform_id` on success or leave `pending` for the retry loop. They
write `SetLastReply` (always) and `BumpEngagement` (unless the active
turn's trigger is `timed-*` — read from `currentTrigger`). `like` /
`edit` / `delete` / `pin` / `unpin` act on a platform message by
`platform_id` and do **not** append a `messages` row (they mutate an
existing platform-side message via the adapter's `Socializer`).

**Adapter egress contract.** Delivery (initial + retry) calls the
owning adapter's `POST /v1/send` with
`{jid, text, reply_to_id, thread_id, idempotency_key}` where
`idempotency_key` is the bot row's stable `message_id`; the adapter
dedups platform-side on that key and returns
`{platform_id, ok}`. routd writes `platform_id` + `status='sent'` on
the `200`. `document` passes `{jid, path, name, caption, reply_to_id,
idempotency_key}`; the file at `path` lives on the shared group
volume routd and the adapter both mount, so routd passes the path (no
byte streaming). If the file is gone at retry time the adapter returns
`404`, routd marks the row `failed` (no infinite retry). This is the
same `POST /v1/send` the `POST /v1/outbound` passthrough uses for
non-agent senders; the only difference is the turn path appends the
row first.

**Batch per turn.** The agent's per-turn output is delivered as a
batch through these calls within one run; `submit_turn`
(§ turn lifecycle) closes the turn. routd sequences appends in call
order so the platform sees the agent's frames in order. Multiple
`reply`s in one turn chain their `reply_to_id` to the prior
`platform_id` (thread continuation).

### Turn lifecycle — `submit_turn` + `turn_results`

The agent calls `submit_turn` once at the end of a run (a hidden
JSON-RPC method on the MCP socket, not in `tools/list` — federated by
`runed`, handled by routd). routd records the outcome idempotently:

```jsonc
// submit_turn (MCP) / POST /v1/turns/{turn_id}/result (REST twin)
{ "turn_id": "wamid.X", "session_id": "uuid", "status": "success"|"error",
  "result": "<final text, optional>", "error": "<optional>",
  "caller_sub": "u_abc", "models": { "claude-…": {"input":1200,"output":340,"cost_cents":2} } }
// 200  {"recorded": true|false}   // false = duplicate (folder,turn_id), ignored
```

`RecordTurnResult(folder, turn_id, session_id, status)` inserts into
`turn_results` (PK `(folder, turn_id)`); a duplicate returns
`recorded:false` and is dropped. On a first record routd looks up
`turn_context[turn_id]` to recover `(folder, topic, chat_jid)` (the
payload carries no `topic`), persists the new `session_id` into
`sessions(folder, topic)`, writes cost rows (to the cost owner),
publishes `round_done` to the web SSE channel (keyed on the chat JID's
folder via `turn_context.chat_jid`, **not** the routing-target folder —
the known routed-web-submission gotcha), and delivers `result` if
present.

**Completion reconciliation (state machine, PINNED).** Two terminal
signals exist for a turn: the `POST /v1/runs` HTTP response
(`outcome` + `session_id`) and `submit_turn`. They reconcile as:

| Order of arrival                                      | Resolution                                                                                                                                                                                 |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `submit_turn` then run-response                       | `submit_turn` is authoritative for `session_id`/`status`; run-response only marks `turn_context.done`                                                                                      |
| run-response `ok`/`silent`, `submit_turn` never       | run-response's `session_id` persists; cursor advances; turn closed                                                                                                                         |
| run-response `error`, then late `submit_turn:success` | the FIRST terminal signal wins for cursor + result delivery; the late `submit_turn` records into `turn_results` (PK dedup) but does NOT re-deliver or re-advance — the turn already closed |
| neither (crash)                                       | `turn_context.state` stays `running`; on restart the chat is re-fed from the un-advanced cursor (the run is re-attempted, `turn_results` dedups if the prior run had recorded)             |

`agent_cursor` advances **once**, at the FIRST terminal signal for the
turn (whichever of run-response / `submit_turn` lands first), gated on
the per-folder serialization so no second turn for the folder starts
until the first closes. `session_id` persistence prefers `submit_turn`'s
value (it reflects the actual session the agent wrote) and falls back to
the run-response's. `turn_context.state` flips to `done` at the first
terminal signal; a duplicate `POST /v1/runs` start for an already-`done`
turn_id (crash/replay) is rejected `409 {"error":"turn_done"}`.

**Post-terminal callbacks.** Conversation callbacks
(`/v1/turns/{turn_id}/*`) remain valid until the `POST /v1/runs` HTTP
request returns, even after an early `submit_turn` flipped the turn to
`done` — the run is still live and may emit trailing frames, and the
single-process per-folder serialization guarantees no second turn for
the folder has started. After the run-response returns (run truly
over), a late callback for that `turn_id` is rejected
`409 {"error":"turn_done"}`. The terminal signal closes cursor/result
accounting; it does not slam the append window mid-run.

**Crash recovery for orphaned turns.** On startup routd scans
`turn_context` for `state='running'` rows: these are turns whose run
outcome was never recorded (routd crashed mid-run). routd does NOT
assume the old run is dead — but because runed containers are
per-turn and exit on their own, and the per-folder queue is empty
after restart, routd simply re-feeds the chat from the un-advanced
`agent_cursor` (`recoverPendingMessages`). A re-dispatched run gets a
**fresh** `turn_id` only if new inbound arrived; for the same trigger
the `turn_id` is identical, so `turn_results` PK dedups any
`submit_turn` the old run still manages to deliver, and the
`409 turn_done` guard blocks a double live run for the same turn_id
once the new one is marked. Stale `running` rows older than the run
timeout are swept to `done` by the hourly GC.

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
// 403 {"error":"forbidden","message":"mint scope by tier (spec 5/W)"}
```

`owner_folder` bounds revocation and is taken from session context for
the MCP face (never a parameter there); the REST face requires it
explicitly and gates on `Authorize(principal, admin, owner_folder)`.
Validation (pinned): `target_folder` MUST equal or be a descendant of
`owner_folder` (`acme` may mint for `acme/eng`; not vice versa).
`source_label` and `jid_suffix` MUST match `[\w-]+` per segment (they
become JID path segments — reject `/`, whitespace, `:`). Multiple
active tokens per `jid` are **permitted** (PK is `token_hash`, not
`jid`) — issuing a second token for the same JID mints a distinct
token, never an error; revocation is per-token (by `jid` deletes all
tokens for that JID under the caller's `owner_folder`). List items are
`{jid, owner_folder, created_at}` (never the raw token); `DELETE`
returns `204` on hit, `404` when no token matches the `(jid,
owner_folder)` pair.

The bearer-token URL surfaces (`GET/POST /chat/<token>/`,
`POST /hook/<token>`) live in **webd**, which reads `route_tokens`
through this api (it does not own the table) — webd hashes the URL
token, looks up the row, and appends the body via `POST /v1/messages`
under the row's JID. No ACL at those surfaces; the token IS the auth
(+ webd's per-token rate limit).

## MCP tool face

routd is **agent-first**: MCP is the canonical protocol, REST is the
impedance match. Every `/v1/*` handler above has an MCP twin where an
agent needs it — **one handler, two faces**
([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)). The agent-facing
conversation tools are **served to the agent via `runed`'s federation**
(runed hosts the per-folder unix socket and forwards the call to routd's
`/v1/turns/{turn_id}/*`); routd is the **handler owner**. routd's own
process also serves the routing-control tools directly.

| MCP tool                                                                         | Face of                                | Owner-served by             | Scope                        |
| -------------------------------------------------------------------------------- | -------------------------------------- | --------------------------- | ---------------------------- |
| `reply`                                                                          | `POST /v1/turns/{id}/reply`            | routd (via runed)           | `messages:send:own_group`    |
| `send`                                                                           | `POST /v1/turns/{id}/send`             | routd (via runed)           | `messages:send:own_group`    |
| `send_file`                                                                      | `POST /v1/turns/{id}/document`         | routd (via runed)           | `messages:send:own_group`    |
| `get_history` / `get_thread`                                                     | `GET /v1/turns/{id}/history`,`/thread` | routd (via runed)           | `chats:read:own_group`       |
| `like` / `dislike` / `edit` / `delete` / `pin_message` / `unpin_message`         | `POST /v1/turns/{id}/{verb}`           | routd (via runed)           | `messages:send:own_group`    |
| `engage` / `disengage`                                                           | engagement write (spec 5/G)            | routd (via runed)           | self/owned jid (3-arm authz) |
| `fork_topic`                                                                     | lineage write (spec 5/F)               | routd (via runed)           | `messages:send:own_group`    |
| `set_routes` / `add_route` / `delete_route`                                      | route CRUD                             | routd direct                | `routes:write:own_group`     |
| `inspect_routing`                                                                | route-table introspection              | routd direct                | `routes:read:own_group`      |
| `issue_chat_link` / `issue_webhook` / `list_route_tokens` / `revoke_route_token` | `/v1/route_tokens`                     | routd direct (or via runed) | tier-scoped (spec 5/W)       |

`reply`/`send` keep distinct names + sharp descriptions (different
intents — threaded answer vs. fresh top-level message), not one tool
with a `mode=` param, per the project tool-naming rule. The
distinction between "owner-served by routd, federated through runed"
and "routd direct" is a deployment detail; the handler is routd's
either way.

## The routd↔runed interface (PINNED)

routd drives the agent run; runed runs it. The contract is pinned —
`runed`'s spec is written to match exactly.

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
  "turn_id": "wamid.X",                 // the triggering inbound id; echoed on the conversation callbacks
  "capability_scopes": ["messages:send:own_group", "chats:read:own_group", "..."],
  "model": "",                          // group override; empty = instance default
  "container_config": { /* opaque GroupConfig forwarded from groups.container_config */ },
  "isolated": false                     // timed-isolated:* runs get a one-off container, no session persist
}
// 200 (sync, run complete)
{ "run_id": "run-…", "outcome": "ok"|"error"|"silent", "session_id": "uuid", "error": "",
  "breaker_open": false }   // true ONLY on the run that trips runed's circuit breaker (P-runed § queue)
```

- **Sync vs streamed-status.** The call is **synchronous** for the
  turn boundary: routd blocks on the HTTP response, which returns when
  the run completes (mirrors today's `runner.Run` return). The agent's
  conversation frames arrive **out-of-band during the run** via the
  callbacks below (so the user sees streamed output), not in this
  response body. `submit_turn` is the canonical end-of-turn signal;
  the `POST /v1/runs` response carries the terminal `outcome` +
  `session_id` as a backstop in case the agent never called
  `submit_turn` (e.g. crash). routd reconciles: a `session_id` /
  `error` from `submit_turn` (recorded under `turnsMu`) wins over the
  response body when both arrive.
- **`outcome`**: `ok` (turn ran, may or may not have replied),
  `error` (run failed — routd advances cursor past the batch, marks
  rows errored, sends the failure notice), `silent` (ran, produced no
  deliverable output — logged, no error).

**Transport semantics (PINNED).**

- **Timeout.** routd applies a hard deadline `RUNED_RUN_TIMEOUT`
  (default = the group's container timeout + grace, instance-tunable).
  On deadline routd cancels the HTTP request; runed must `Stop` the
  container (the `ContainerRuntime.Stop` graceful-then-kill path) when
  its request context cancels. A timed-out run is treated as
  `outcome:error` for cursor purposes (advance past the batch — the
  same starvation guard the gateway has today).
- **Cancel.** Cancellation is request-scoped only: routd cancels by
  dropping the HTTP request (context cancel); there is no separate
  `DELETE /v1/runs/{run_id}`. runed owns its lifecycle and reaps on
  context cancel.
- **`run_id` is runed-minted and unique per call.** routd never
  pre-assigns it; it is for log correlation, not idempotency. Run
  idempotency is keyed on `turn_id` (the `turn_context` PK +
  `409 turn_done` guard above), NOT `run_id`.
- **Transport failure vs `outcome:error`.** A transport failure (TCP
  reset, 5xx, timeout) is **distinct** from a clean `outcome:error`
  body: transport failure means routd doesn't know whether the run
  happened, so it does NOT advance the cursor and the chat is re-fed on
  the next poll (at-least-once; `turn_results` dedups a redundant
  `submit_turn`). A clean `200 {outcome:error}` means the run
  definitively failed — routd advances past the batch (no infinite
  replay). `POST /v1/runs` is therefore **NOT** blindly retried by
  routd; re-attempt happens only through the normal poll re-feed, gated
  by per-folder serialization.

**runed → routd: the agent's conversation tools call back.** The
agent's `reply`/`send`/`get_history`/… tools (hosted by runed's
federation, executing inside runed's container) call **back** into
routd's `/v1/turns/{turn_id}/*` (above). routd is the sole appender;
runed never writes a `messages` row. The `turn_id` on every callback
binds it to the run routd started.

**Invariants.** routd never spawns a container or holds a Docker
handle. runed never opens `routd.db` or appends a message. The two
talk only over these HTTP contracts. The `ContainerRuntime` seam
([`U-genericization.md`](U-genericization.md) § ContainerRuntime) lives
entirely inside runed.

## Auth

routd is a **verifier, not a signer**. It holds no signing key; it
offline-verifies tokens against authd's cached JWKs via the `auth/`
library ([`1-auth-standalone.md`](1-auth-standalone.md)). Two distinct
credential classes cross routd's boundary — keep them separate:

| Credential             | Issued by | Verified by routd how                                    | Used for                                              |
| ---------------------- | --------- | -------------------------------------------------------- | ----------------------------------------------------- |
| Agent capability token | `authd`   | `auth.VerifyHTTP` offline against cached JWKs            | `/v1/turns/*`, `/v1/routes` (agent), conversation MCP |
| Adapter/service token  | `authd`   | same offline verify; `sub = service:<adapter>`           | `POST /v1/messages` ingress, `POST /v1/outbound`      |
| **Route token** (5/W)  | **routd** | `sha256(token)` lookup in `route_tokens` (routd's table) | webd's `/chat/<token>/`, `/hook/<token>` surfaces     |

Route-tokens are **routd-owned bearer credentials, DISTINCT from agent
capability tokens.** A route token is a 32-byte opaque secret stored
hashed; it is not a JWT, carries no scope, and authd never sees it. It
authorizes exactly "append at this JID" at the public web surface —
nothing else. An agent capability token is an authd-minted ES256 JWT
with `scope` + `arz/folder` that gates the `/v1/*` API. Verifying one
is never a path to the other.

`routd` obtains its own `service:routd` token at boot
(`auth.ServiceToken` against authd) to authenticate its daemon→daemon
calls (the `POST /v1/runs` to runed, reads against runed / cost owner).
The HMAC `CHANNEL_SECRET` / `PROXYD_HMAC_SECRET` paths are gone —
retired in the authd cutover that precedes this split
([`1-auth-standalone.md`](1-auth-standalone.md) § HMAC retirement).

## Standalone-ready acceptance

One contract test (the [`U-genericization.md`](U-genericization.md)
§ Acceptance bar for `routd`), in `tests/standalone/routd_test.go`:

> Boots with `DB_PATH=/tmp/routd.db` and a stub `RUNED_URL`; runs its
> own migrations; accepts an inbound event via `POST /v1/messages`;
> resolves a route from a single `PUT /v1/routes` rule; the loop
> dispatches a (stub) run by calling `POST <RUNED_URL>/v1/runs` and
> records the `submit_turn` outcome in `turn_results`. No `core.Folder`
> leak / no arizuko-domain hardcoding in the binary beyond `types.*`
> (cross-boundary signatures use `types.Folder` / `types.UserSub` /
> `types.Scope`; the wire shape may still render the field as
> `tenant_id` while the Go type is `types.Folder`).

The stub runed records the `POST /v1/runs` body and replies
`{outcome:"ok", session_id:"stub"}`, then calls back
`POST /v1/turns/{turn_id}/reply` to prove the sole-appender callback
path. Asserts: (1) the inbound row exists in `messages`; (2) the route
resolved to the expected folder; (3) runed was called with the
rendered batch + `turn_id`; (4) the callback reply appended exactly one
bot row `is_bot_message=1`; (5) `turn_results` has one row for
`(folder, turn_id)` and a duplicate `submit_turn` is dropped.

## What this spec is not

- **Not the container runner.** runed owns spawn/kill/stdio and the
  `ContainerRuntime` seam. routd calls `POST /v1/runs` and never
  touches Docker.
- **Not the MCP host.** runed owns the per-folder unix socket and tool
  federation; routd owns the conversation/routing **handlers** runed
  federates.
- **Not the signer.** authd mints; routd offline-verifies. Route
  tokens are routd's only minted credential, and they are bearer
  secrets, not JWTs.
- **Not a per-channel adapter.** Adapters stay edge daemons; they
  reach routd via `POST /v1/messages` and receive egress via the
  adapter `/v1/*` that routd's delivery calls.
- **Not a backward-compat layer.** One-shot multi-DB cutover with
  routd + runed; recovery is `git revert`
  ([`U-genericization.md`](U-genericization.md) NO BACKWARD
  COMPATIBILITY).

## Code pointers

- `gateway/gateway.go` — the loop (`pollOnce`, `processGroupMessages`,
  `processSenderBatch`, `resolveOrEngaged`, `makeOutputCallback`,
  `handleSubmitTurn`, `publishRoundDone`, `issueRouteToken`). This is
  routd's core; the `GatedFns`/`StoreFns` seams become routd's `/v1/*`
  - MCP handlers, the `runAgentWithOpts` body becomes the `POST
/v1/runs` call to runed.
- `api/api.go` — `handleMessage` (ingress), `handleOutbound`, route-token
  REST handlers, the 5/L promotion + 5/G engagement-on-mention block.
- `router/router.go` + `core.ParseRouteTarget` — route resolution,
  `RouteMatches`, `#observe`/`#topic` fragment parsing, `FormatMessages`
  / `FormatOutbound` / `ExtractStatusBlocks`.
- `store/` — every table above (`store/messages.go`, `store/groups.go`,
  `store/routes.go`, `store/web_routes.go`, `store/route_tokens.go`,
  the topic-lineage + engagement helpers). The access methods become
  routd's internal DB layer.
- `ipc/ipc.go` — the conversation + routing MCP tool definitions
  (`reply`, `send`, `get_history`, `get_thread`, `set_routes`,
  `add_route`, `engage`, `disengage`, `fork_topic`, `submit_turn`,
  route-token tools) — the MCP face routd serves (directly or via runed).
- `runed`'s spec (companion) — the `POST /v1/runs` peer of § The
  routd↔runed interface; `container/runner.go` is its core.

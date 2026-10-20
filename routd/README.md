# routd

Conversation/routing plane: the message store + routing rules +
orchestration loop + channel ingress/egress.

## Purpose

The conversation state machine. routd is the **sole appender** of
messages and a token **verifier**, not a signer (authd signs). It owns
the `routd` tables, resolves routing rules, runs the orchestration loop, and
calls runed (`POST /v1/runs`) to execute each turn. It also hosts the
per-turn agent MCP socket **in-process** (`ServeTurnMCP`, `mcp.go`). runed
starts no MCP server: its bridge carries the guest's tool calls to this
socket (`runed/README.md` § The peer credential).

## Responsibilities

- Ingest inbound from channel adapters; append messages (sole appender).
- Resolve `routes` (topic / sticky / reply rules) to a target folder.
- Run the orchestration loop: claim a turn, dispatch to runed, track results.
- Host the in-process per-turn MCP socket: resolve the folder's container
  capabilities from `acl`, wire `ipc.ServeMCP` to routd's DB + Deliverer,
  and serve the agent's conversation tools (`reply`/`send`/`like`/…)
  in-process. routd injects the agent authz gate here — `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` — and gates
  tool visibility via `auth.EffectiveActions`; resreg-served cold-tier
  resources reuse that injected gate instead of their default operator
  gate.
- Own the turn's progress (`progress.go`): the
  only code that calls `Typing` or creates or edits a `verb="status"` row.
  `submit_status` (`mcpSubmitStatus`) and each `<status>` block in a result
  both call `updateProgress`: the first update sends one `⏳` message, a later
  one EDITS it and rewrites the row, an update whose text the row already holds
  does nothing. A channel that does not advertise `edit` gets no progress
  message; a real edit failure surfaces and is never re-posted (spec 5/24). The
  message id is the newest `verb="status"` row's `platform_id`, so
  edit-in-place survives a restart; the row is stamped `verb="status"` so
  `TurnHasBotReply` excludes it — a status must never count as the agent's
  reply. `Typing(on)` fires at dispatch and `Typing(off)` when the turn ends
  (`finishTurn`, the one writer of `done`), not at the first reply; `finishTurn`
  also edits the message's leading `⏳` to `✓` or `✗`. A run that returns
  without ending its turn clears `Typing` and leaves the message `⏳`.
- Channel egress via the `chanreg` Deliverer (adapters register their
  egress URL + owned JID prefixes).
- Answer ranked message search, `find_messages` and
  `GET /v1/messages/find` (§ Ranked message search).

## The turn in routd

The figure in `ARCHITECTURE.md` § Message Flow shows the whole turn. routd's
half keeps these rules.

- **One cursor.** routd is the sole appender of `messages`, so one cursor,
  `chats.agent_cursor`, means "what the agent has seen". A second writer
  would need a second cursor.
- **One process.** routd is single-process per instance, and two routd on one
  store are unsupported. The in-memory `GroupQueue` is the arbiter of one turn
  per folder, and `agent_cursor` recovers a crash.
- **Ingress.** `POST /v1/messages` honours `X-Idempotency-Key` only when the
  body carries no `id`: routd mints `id=<adapter>-<key>`, so a re-sent event
  lands on the same row. Both at once is `400 ambiguous_idempotency`
  (`server.go`). Engagement is not claimed at ingress, because the folder is
  unknown until resolution; a claim with an empty folder would misroute.
- **Resolution.** The order and its two stages are in `ROUTING.md`
  § Resolution Order. A route-table read failure is logged at ERROR and
  treated as a miss: a retry risks a poison-message loop.
- **The run contract.** `POST /v1/runs` is synchronous, and
  `RUNED_RUN_TIMEOUT` plus two minutes is routd's own deadline on it.

  | runed answers                                      | routd does                                                                         |
  | -------------------------------------------------- | ---------------------------------------------------------------------------------- |
  | `ok`                                               | advances the cursor and the observed cursor, ends the turn                         |
  | `silent`                                           | the same, and counts no output                                                     |
  | `ok` with no result and no bot row                 | tells the chat, never retries: a clean run that delivered nothing is a malfunction |
  | `error`, no reply, not `terminal`                  | retries, up to `MAX_TURN_RETRY`                                                    |
  | `error` with a reply, retries spent, or `terminal` | marks the rows errored, sends one notice to every chat the run owed, advances      |
  | `busy`                                             | no advance, no breaker charge, the busy backoff                                    |
  | `steered`                                          | ends this turn; the live run owns the batch                                        |
  | a transport failure                                | no advance; the next poll re-feeds the batch                                       |

- **Busy backoff.** A busy refusal parks the batch for 4 s, doubling per
  repeat refusal of the same batch to a 60 s cap (`dispatch.go`). A new user
  message clears it; the running turn's own bot rows do not. A failed turn's
  retry returns through the same busy branch, so it waits on this backoff
  too. A transport failure and a dependency outage do not: they re-feed on
  the next poll.
- **Terminal.** runed decides whether a failure is terminal, and routd never
  parses runed's text to decide it. A terminal notice names the cause: the
  agent's own reported failure, else runed's error text.
- **Retry state.** `turn_context.retry_count` and `state='pending_retry'`
  survive a routd restart. A retry turn starts with a note that it is a retry.
- **Turn lifecycle.** `submit_turn` is hidden from `tools/list` and recorded
  once: the `turn_results` key dedups it, so the first terminal signal wins
  the cursor and the delivery. A `running` turn older than the run timeout
  sweeps to `expired`, never `done`: crash recovery keys on `running`, and the
  guard against a second live run keys on `done`.
- **One turn per folder.** Concurrent inbounds for a busy folder are steered
  into the live guest or wait for the queue. The observed cursor is
  at-least-once: it advances on the same success path as the message cursor,
  so a re-fed turn sees the context the failed attempt saw.
- **Outbound.** The pending bot row is the outbox; `chanreg` and the adapters
  hold no queue. A sweep every 30 s re-sends `pending` rows, and each row waits
  its own gap: 30 s, doubling per failure to a 1 h cap, so ten attempts span
  about an hour. A flat 30 s cadence spent the budget in five minutes. A 4xx
  dead-letters at once with `delivery_attempts` 0; a transient failure spends
  one attempt up to `MAX_OUTBOUND_RETRY`. Age is not a bound: an age rule
  discarded rows the sweep never tried.
- **A failed send writes its row.** MCP `send`, `reply` and `send_file`
  return the error to the agent and record the undelivered row: `pending` for
  a transient failure, `failed` for a permanent one. A failed `send_file`
  always dead-letters (`ipc/ipc.go` `recordOutboundFailure`).
- **Delivery dedup** is the adapter's: `chanlib/README.md` § Delivery is
  claimed before the platform call.

## Ranked message search

The index is an in-memory BM25 index over the `messages` entity
(`msgsearch/`). It is derived, and the store is the record.

- **Built at boot.** The process that answers searches, routd's own `main`,
  calls `StartSearch`, which walks every chat into the index in the
  background. A search before the walk finishes is refused with
  `index_warming`, never answered with an empty hit list: an agent repeats
  "no message says that" as fact.
- **Fed by four writes.** routd is the sole appender, so the feed is one line
  beside each write that touches an indexed attribute: the append, the media
  rewrite, the observe stamp, the breaker's prune. Each runs after the store
  write commits. A write another process makes, `arizuko send`, an archive
  import or a group delete, appears at the next boot walk.
- **Tokens.** A term is a run of Unicode letters and digits, lower-cased, with
  diacritics dropped after NFD, so `uroven` finds `úroveň` from a plain
  keyboard. Each token keeps its byte offsets, so the snippet marks the hit in
  the bytes the writer wrote.
- **Rank and snippet.** BM25 with SQLite FTS5's constants, `k1` 1.2 and `b`
  0.75, negated as FTS5 negates it, so ascending rank is best first
  (`parity_test.go`). A hit returns a window of at most 32 tokens around the
  first match, each hit inside `«` and `»`. Default 20 hits, at most 200.
- **Grammar.** Terms, all required; `"phrases"`, adjacent and in order; `-`
  before a term or phrase excludes it. No OR, no prefix, no NEAR. A malformed
  query is an error, never an empty list.
- **Scope.** A `scope` holding `:` is a chat; anything else is a folder
  subtree over `routed_to`, tested with `groupfolder.Contains`, never a
  `LIKE`. The caller filters the hits to the folders it may read.

## The model gateway

With `MODEL_GATEWAY_URL` set (the `litellm` package), the gateway holds the
model credential and is the spend ledger; routd stores no spend.
`gateway.go` and `budget.go` map arizuko's nouns onto the gateway's:

| arizuko                  | gateway                                              | why                                                                                                                                       |
| ------------------------ | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| folder                   | `team_id`, verbatim                                  | the id IS the folder, so no folder→team row exists to drift from a wiped gateway                                                          |
| `cost_cap_cents_per_day` | team `max_budget`, `budget_duration "1d"`            | the meter enforces the cap on the request                                                                                                 |
| person                   | internal `user_id`, set by routd at mint             | the body `user` field and the `x-litellm-end-user-id` header are written in the guest, so the spender would choose whose budget it spends |
| turn                     | one key: TTL = the run ceiling, no budget of its own | revoke is deleting the key; `turn_context.gateway_key_hash` answers `GET /v1/cost?turn_id=`                                               |

- **One `GET /team/info` per turn.** The pre-spawn gate creates the team when
  the gateway has none, because a key naming an unknown team is accepted and
  then every model call answers 404. The same call converges the team's
  budget on the operator's cap and reads the spend the refusal quotes. The
  gate runs with `COST_CAPS_ENABLED` off too: off reports both caps as
  uncapped and clears them at the gateway, or the gateway would keep
  refusing turns arizuko does not cap.
- **A spent cap refuses the next turn.** The refusal goes to the chat and
  consumes the batch. There is no mid-turn budget notice: an over-cap turn
  runs to its end and the next turn's gate refuses. A call outside the
  gateway is a log line, never a row.
- **The gateway failing is a dependency outage.** A gate read or a key mint
  that fails returns `queue.ErrDependencyDown` (`dispatch.go`): no guest
  starts, the batch re-feeds and the folder's breaker streak stays as it was.
  A turn never runs unmetered. A failed spend read is an error, never an
  empty spend.
- **Limits.** A message that steers into a running guest spends that run's
  key, so its tokens land on the first turn's key and user. A user's own model
  key is dropped under a gateway, with a warning in runed's log, because
  nothing translates it into a gateway key (BUGS `L404`).
- **No gateway, no meter.** The dashboard says so instead of a number the
  agent chose. A cap on such an instance is enforced by nothing, and the gate
  logs that on every turn.

The package ships its own Postgres (`litellm_db`) for keys and spend: the
gateway's bookkeeping, not the instance's store. Every operator surface
reads spend back through `/v1/cost/summary`.

## Tables owned

The `routd` owner — every one an objstore entity, made at boot by routd
itself (`Open` → `store.PrepareOwner`):

- Message store: `messages` plus the four read-shaped indexes an append fans
  out into (`message_folders`, `message_folder_counts`, `message_activity`,
  `message_delivery`); `chats`, `chat_reply_state` and `chat_proactive`.
  Ranked search over the message content is memory, not a table (`msgsearch/`)
- Routing: `routes`, `web_routes`, `route_tokens` (`kind='route'`
  delivery bearers + `kind='pair'` identity-pairing links, spec 5/31)
- Orchestration: `turn_context`, `turn_results`, `sessions`,
  `system_messages`, `group_watchers`
- Auth+grants: `acl`, `acl_membership`, `secrets`, `secret_use_log`
- Scheduler: `scheduled_tasks`, `task_run_logs`
- Slack pane: `pane_sessions`
- Held tool calls: `pending_actions`; webhook sources:
  `github_sources` (spec 6/27); proxyd's route table: `proxyd_routes`,
  which proxyd owns every write of and serves on its own `/v1`
- Counters: `counters` — one server-minted number, `routes.id`
- People: `user_profiles` — the username a person's folder is named after
  and their per-day spend cap (authd owns identity). Spend itself belongs to
  the model gateway (§ The model gateway).
- Network: `network_rules`
- Packages: `installed_packages` (installed-package record, keyed
  (folder, name) with `''` = instance-wide; the CLI writes it directly,
  split write-discipline — spec 5/28)

routd reaches NO other owner's tables — cross-daemon data arrives over HTTP
(authd identity, runed session_log).

`/openapi.json` advertises exactly what routd mounts over REST
(`OpenAPIResources`, `server.go`): `routes`, `web_routes`, `acl`,
`secrets`, `route_tokens`, `installed_packages`, `scheduled_tasks`,
`groups`.
`secrets` IS included but declares write-only endpoints (create +
key-delete, no read op), so `enc_value` cannot leak through a convention
`GET`; `installed_packages` declares only its two GETs.
`scheduled_tasks` is the one resource whose REST path is not
`/v1/<name>` — it serves `/v1/tasks` because timed calls that prefix
across the container boundary (`timed/dash.go`, and the fire loop's
`/v1/tasks/due`, `/runlog`, `/{id}/reschedule`).
An agent-only action is declared `MCPOnly` on the resource's ONE
`Endpoints` slice, so mounting and emission read the same list and the
doc can advertise nothing routd doesn't serve: `scheduled_tasks`'
schedule/pause/resume, `route_tokens`' issue_pair, `acl`'s list
(`list_acl`), `groups`' register (`register_group` — creation stays
dashd's `SetupGroup`). Omitted: `network_rules` (all three actions
`MCPOnly`, so it has no REST face to advertise) and `acl_membership`
(dashd-FS-managed).
`acl_membership` exposes exactly one face-pair: `unpair` (MCP) and
`DELETE /v1/acl_membership` (REST), both scoped to `added_by='pairing'`.
Writing that edge is not an endpoint at all — it happens only when a
human confirms a pairing link in a browser (spec 5/31).

## Entry points

Binary: `routd/cmd/routd/main.go`. Listen: `:8080` (`LISTEN_ADDR` default).

Endpoints (`server.go`, `channels.go`):

- `POST /v1/messages` — inbound from adapters (`messages:write`)
- `POST /v1/outbound` — adapter passthrough (`messages:write`)
- `GET|PUT|POST|DELETE /v1/routes` — route CRUD
- `GET|PUT|DELETE /v1/web_routes` — web route CRUD
- `GET /v1/web_presence` — folder vhost info (get_web_presence REST twin)
- `POST /v1/route_tokens/{chat,hook}` — mint chat/hook route tokens
  (optional `context` = per-link processing instructions, spec 5/W)
- `GET /v1/route_tokens`, `DELETE /v1/route_tokens/{jid}` — list/revoke.
  Token DELIVERY has no REST face: webd and proxyd read routd's tables
  directly and resolve the URL token in-process (spec 5/W)
- `DELETE /v1/acl_membership` — unpair; the caller's sub must BE the
  `parent` (spec 5/31). The agent twin is the `unpair` MCP tool, whose
  containment is the child JID's routing folder instead
- MCP-only: `issue_pairing_link(jid)` mints a `kind='pair'` route token
  for a JID that routes to the caller's folder. No REST twin by design
  — no operator mints a pairing for a chat they are not in (spec 5/31)
- Chat `/approve`, `/reject`, `/always` verdicts resolve at ingress, including
  native approval buttons. They reach a held call while its agent turn waits;
  the sender needs admin on the held call's folder. `pending_actions` has a
  REST face (list, approve, reject) and no agent-socket face, because an
  agent that could approve would approve its own call. Both faces resolve
  through `resolveHoldInto`.
- `POST /v1/channels/register`, `POST /v1/channels/deregister` — adapter registry
- `GET /v1/channels` — adapter list
- `GET /v1/messages/{inspect,thread,find}` — message reads
- `GET /v1/routing/{resolve,errored}` — routing info
- `GET /v1/routing/answering?since=` — whether agents are ANSWERING people over
  a window (default 7 days): turn outcomes by state, and every chat whose newest
  human message is newer than its newest agent reply. Each waiting chat carries
  the loop's own verdict — `route` (a turn was due, so waiting is a fault),
  `observe` or `held` (the chat answers nothing by the operator's own
  configuration, e.g. a `verb=mention` rule and a plain message). An aggregate,
  never message content; a folder-scoped bearer sees its own subtree and no
  unclaimed chats
- `GET|POST /v1/engagement` — engagement state (spec 5/G). `GET` with `jid`
  reads one pair (`folder`, `engaged_until`, `last_reply_id`); `GET` with no
  `jid` LISTS the live windows in the caller's subtree, newest deadline first
  — only an empty (root/service) folder claim widens it to the fleet. `POST`
  engages; `ttl_seconds <= 0` disengages. The write is contained on the LIVE
  window's CLAIMING folder (the body's `folder` is caller-supplied and does not
  widen it), so it can never reach a window the list would not show, and it
  lands its `audit_log` row inside the write's own transaction
  (`DB.SetEngagementAudited`) — `engagement.set` / `engagement.clear`.
  Disengagement survives the current turn's final answer and implicit thread
  mentions. A fresh mention or explicit engagement can start another task.
- `GET /v1/topic_sessions` — the resume Claude-session id for (folder, topic)
- `GET /v1/users/{sub}/scopes` — login-time scope snapshot (`grants:read`)
- `POST /v1/acl`, `DELETE /v1/acl` — acl grant/revoke (`acl:write`)
- `POST /v1/secrets`, `DELETE /v1/secrets/{key}` — secret write/delete (`secrets:write`)
- `POST /v1/pane` — Slack pane context (`messages:write`)
- `GET|PATCH|DELETE /v1/tasks[/{id}]` — scheduled-task CRUD, shared with the agent's task tools (`tasks:read`/`tasks:write`)
  `schedule_task(targetJid=...)` accepts a registered folder for internal work
  or a routed external chat. Silent memory reducers target their owner folder.
- `GET /v1/tasks/due` — claim due tasks (`tasks:read`)
- `POST /v1/tasks/runlog`, `POST /v1/tasks/{id}/reschedule` — task logs/reschedule (`tasks:write`)
- `GET /v1/dead_letters` — outbound routd gave up on, with the lost content
  (`dead_letters:read`); `POST /v1/dead_letters/{id}/replay` requeues one for
  the EXISTING outbound sweep (`dead_letters:write`). Its own scope pair, not
  `messages:*`: `messages:write` composes any inbound, this moves one already
  composed and already failed row back to `pending`. `delivery_attempts` tells
  the two dead-letter causes apart — 0 = the platform refused it, the cap =
  retries ran out. Agent twins `list_dead_letters` / `replay_dead_letter`.
- `GET /v1/cost?turn_id=` — what the gateway charged one turn's key;
  `GET /v1/cost/summary?folder=&since=` — what it metered per folder over a
  window (`cost:read`, `cost:read:own_group`). Both read the gateway; 404 or
  an explicit "no meter" when the instance has none
- `GET /openapi.json` — OpenAPI 3.1 doc (public, pre-auth)
- `GET /health` — healthcheck (public)

## Dependencies

- `auth` (offline token verify via authd JWKS), `chanreg` (channel
  registry + Deliverer), `ipc` (`ServeMCP`), `grants`, `groupfolder`,
  `obs`, `resreg`, `types`
- `authd` (JWKS), `runed` (`POST /v1/runs`), channel adapters (ingress/egress)

## Configuration

Core vars:

- `DATA_DIR` — groups/web/tts dirs, and the owner file when the instance
  keeps one (`DYNAMODB_URL` unset)
- `LISTEN_ADDR` — HTTP listener (default `:8080`)
- `AUTHD_URL` — JWKS source; unset → open mode (local-dev, no verifier)
- `AUTHD_SERVICE_KEY` — service:routd ES256 token exchange key; unset →
  fallback to `ROUTD_SERVICE_TOKEN` (additive)
- `ROUTD_SERVICE_TOKEN` — static service token (deprecated; ES256 preferred)
- `RUNED_URL` — runed runs endpoint (default `http://runed:8080`)
- `RUNED_RUN_TIMEOUT` — container kill deadline (default 20m); routd waits +2m
- `ONBOD_URL` — onbod federation for `/invite`, `/gate` commands
- `WEB_HOST` — canonical web origin
- `SECRETS_KEY` — keyring for decrypt-only secret reads; unset → ciphertext

Behavior:

- `ASSISTANT_NAME` — instance identity (default "Andy")
- `ENGAGEMENT_TTL` — reply-state engagement window (default 30m; `0` disables automatic engagement)
- `SESSION_IDLE_EXPIRY` — session reset threshold (default 2d)
- `ARIZUKO_DEFAULT_MODEL` — fallback when group has none (default "claude-opus-4-8")
- `COST_CAPS_ENABLED` — pre-spawn budget gate (default "true")
- `MAX_TURN_RETRY` — SIGKILL/OOM/timeout retry ceiling (default 3)
- `MAX_OUTBOUND_RETRY` — transient delivery failures a `pending` bot row
  absorbs before the sweep dead-letters it to `failed` (default 10). A
  permanent refusal (4xx) dead-letters at once and spends no attempt.
- `OBSERVE_WINDOW_MESSAGES` — prompt context msg count (default 10)
- `OBSERVE_WINDOW_CHARS` — prompt context char ceiling (default 4000)
- `SEND_DISABLED_GROUPS` — CSV folders; persist outbound but don't deliver
- `SEND_DISABLED_CHANNELS` — CSV JID prefixes; mute channel egress

Media enrichment (download + Whisper):

- `MEDIA_ENABLED` — inbound attachment download+transcription (default "false")
- `MEDIA_MAX_FILE_BYTES` — file size cap (default 20MB)
- `WHISPER_BASE_URL` — Whisper service URL (default `http://localhost:8080`)
- `WHISPER_MODEL` — Whisper model name (default "turbo")
- `VOICE_TRANSCRIPTION_ENABLED` — voice→text (default "false")
- `VIDEO_TRANSCRIPTION_ENABLED` — video→text (default "false")

Voice synthesis (send_voice MCP tool):

- `TTS_ENABLED` — toggle send_voice (default "false")
- `TTS_BASE_URL` — TTS service URL (default `http://ttsd:8880`)
- `TTS_VOICE` — voice ID (default "af_bella")
- `TTS_MODEL` — TTS model (default "kokoro")
- `TTS_TIMEOUT` — synthesis deadline (default 15s)

Vhosts (get_web_presence MCP tool):

- `HOSTING_DOMAIN` — root domain for derived vhosts
- `WEB_VHOST_ALIASES` — CSV `host=folder` aliases

Integrations:

- `ONBOARDING_ENABLED` — chat-initiated onboarding (default "false")
- `ONBOARDING_PLATFORMS` — CSV prefixes allowing auto-onboarding
- `CONNECTORS_TOML` — MCP connector catalog path (default `DATA_DIR/connectors.toml`)
- `AUDIT_ENABLED` — optional `audit-system.jl` observability webhook (default "false"); the canonical `audit_log` table is always on

## Health signal

`GET /health` returns 200 once the process is up. Red flag: turns stuck
unclaimed in the loop, or runed unreachable on dispatch.

## Observability

slog → stderr (JSON) + journald. OTLP export when `OTEL_EXPORTER_OTLP_ENDPOINT`
is set (see `obs/README.md`). Every slog event carrying
`turn_id` gets a deterministic TraceID so the collector groups one turn's span.

Audit source of truth is the `audit_log` entity in routd's own tables: every
state-changing MCP tool + REST handler adds one row (`audit.WriteFor`) to the
mutation's own batch, so the row IS the mutation — a refused batch records
nothing and a recorded row happened. `audit-system.jl` (gated by `AUDIT_ENABLED`) is a separate,
optional observability webhook — not the source of truth.

## Files

- `cmd/routd/main.go` — daemon wiring, config, verifier, loop, channel registry
- `server.go` — HTTP mux + auth gates (`Handler`, `authz`)
- `channels.go` — adapter register/deregister + channel health loop
- `loop.go` — orchestration loop (poll/claim/dispatch)
- `dispatch.go` — turn dispatch to runed + enrichment + budget gate
- `turns.go` — the turn's outbound: prose-result + status delivery, `submit_turn` recording
- `deliver.go` — channel Deliverer (egress fanout via chanreg)
- `mcp.go` — in-process per-turn MCP socket (`ServeTurnMCP`, `buildGatedFns`)
- `prompt.go`, `prompt_db.go` — prompt envelope build (observe window, persona)
- `proactive.go`, `steer.go` — proactive interjection + slash-command dispatch
- `routes_http.go`, `tokens_http.go`, `web_routes_http.go`, `tasks_http.go`, `reads_http.go` — REST CRUD
- `*_resource.go` — the thirteen resreg resources (`acl`, `audit`,
  `dead_letters`, `github_sources`, `groups`, `membership`, `network_rules`,
  `packages`, `route_tokens`, `routes`, `scheduled_tasks`, `secrets`,
  `web_routes`). One handler each; agent MCP and operator REST ride the same
  handler with a different injected Gate (spec 5/16)
- `packages_store.go` — `installed_packages` CRUD (the record the CLI's `packages install|upgrade|remove` reads/writes, spec 5/28)
- `db.go` — the owner's writes (PutMessage, turn claims, routing) and the
  boot that dials the endpoint or opens the file
- `search.go`, `msgsearch/` — ranked message search: the index, its boot walk and the four writes that feed it
- `reads.go` — the owner's reads (messages; the chat, session and reply-state reads go through `store`)
- `session.go`, `identity.go`, `onbod.go` — federation to runed/authd/onbod
- `connectors.go` — MCP connector catalog loader
- `enrich.go` — inbound media download + Whisper transcription
- `tts.go` — send_voice synthesis via TTS service
- `dash.go`, `dash_reads_http.go` — `/dash/routd/`: the overview plus the retry
  and dead-letter replay forms, and the reads behind them (spec 6/30). Message
  bodies are cut to 80 characters before they render, here and in the
  `/v1/messages/activity` preview dashd reads. The cut is privacy, not layout:
  an operator traces routing without reading the conversation. The owner cuts
  it, so no consumer can widen it.
- `pending_actions.go`, `pending_actions_http.go` — the HITL hold queue
- `unrouted.go`, `dead_letters_db.go` — the two holds an operator has to see:
  a chat no rule claims, and a message delivery gave up on
- `gateway.go`, `budget.go` — the model gateway's per-turn virtual key and the
  folder spend gate (§ The model gateway)
- `surrogate_refresh.go` — the near-expiry refresh behind surrogate OAuth
- `sibling_db.go`, `ext.go`, `channels.go`, `network.go`, `tokens.go` — helpers

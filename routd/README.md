# routd

Conversation/routing plane: the message store + routing rules +
orchestration loop + channel ingress/egress.

## Purpose

The conversation state machine. routd is the **sole appender** of
messages and a token **verifier**, not a signer (authd signs). It owns
the `routd` tables, resolves routing rules, runs the orchestration loop, and
calls runed (`POST /v1/runs`) to execute each turn. It also hosts the
per-turn agent MCP socket **in-process** (`ServeTurnMCP`, `mcp.go`) —
runed sets `Input.ExternalMCP=true` and only mounts the ipc dir.
Spec: `specs/5/E`.

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
- Own the turn's progress (`progress.go`, `specs/6/46-turn-progress.md`): the
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
- Answer ranked message search — `find_messages` and
  `GET /v1/messages/find` — from an in-memory BM25 index over the
  `messages` entity (`msgsearch/`). The index is DERIVED: the process that
  answers searches — routd's own `main` — calls `StartSearch`, which walks
  every chat into it in the background, and it is fed by the four writes that
  touch an indexed attribute — the append, the media rewrite, the observe
  stamp, the breaker's prune. A search made before the walk finishes is
  refused, never answered with an empty hit list, and a write another
  process makes is picked up by the next walk. Spec `6/42` §8.

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
- Held tool calls: `pending_actions` (spec 5/19); webhook sources:
  `github_sources` (spec 6/27); proxyd's route table: `proxyd_routes`,
  which proxyd owns every write of and serves on its own `/v1`
- Counters: `counters` — one server-minted number, `routes.id`
- People: `user_profiles` — the username a person's folder is named after
  and their per-day spend cap (authd owns identity). Spend itself belongs to
  the model gateway (spec 6/39).
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
  the sender needs admin on the held call's folder.
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
- `POST /v1/cost` — cost log write
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
- `ENGAGEMENT_TTL` — reply-state engagement window (default 30m; `0` disables automatic engagement). GitHub uses explicit routes; see [gitd's trigger policy](../gitd/README.md#observe-vs-trigger).
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
is set (see `obs/`, `specs/5/O-observability.md`). Every slog event carrying
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
- `turns.go` — explicit delivery completion, one shared nudge decision, `submit_turn` recording
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
- `pending_actions.go`, `pending_actions_http.go` — the HITL hold queue (spec 5/19)
- `unrouted.go`, `dead_letters_db.go` — the two holds an operator has to see:
  a chat no rule claims, and a message delivery gave up on
- `gateway.go`, `budget.go` — the model gateway's per-turn virtual key and the
  folder spend gate (spec 6/39)
- `surrogate_refresh.go` — the near-expiry refresh behind surrogate OAuth
- `sibling_db.go`, `ext.go`, `channels.go`, `network.go`, `tokens.go` — helpers

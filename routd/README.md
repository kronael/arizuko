# routd

Conversation/routing plane: the message store + routing rules +
orchestration loop + channel ingress/egress.

## Purpose

The conversation state machine. routd is the **sole appender** of
messages and a token **verifier**, not a signer (authd signs). It owns
`routd.db`, resolves routing rules, runs the orchestration loop, and
calls runed (`POST /v1/runs`) to execute each turn. It also hosts the
per-turn agent MCP socket **in-process** (`ServeTurnMCP`, `mcp.go`) —
runed sets `Input.ExternalMCP=true` and only mounts the ipc dir.
Spec: `specs/5/E`.

## Responsibilities

- Ingest inbound from channel adapters; append messages (sole appender).
- Resolve `routes` (topic / sticky / reply rules) to a target folder.
- Run the orchestration loop: claim a turn, dispatch to runed, track results.
- Host the in-process per-turn MCP socket: derive folder grant rules
  (`grants.DeriveRules`), wire `ipc.ServeMCP` to routd's DB + Deliverer,
  and forward the agent's conversation tools (`reply`/`send`/`like`/…)
  back through `/v1/turns/{turn_id}/*`.
- Channel egress via the `chanreg` Deliverer (adapters register their
  egress URL + owned JID prefixes).

## Tables owned

`routd.db` (routd owns + migrates its own schema):

- Message store: `messages`, `chats`, `engagement`
- Routing: `routes`, `web_routes`, `route_tokens`
- Orchestration: `turn_context`, `turn_results`
- Auth+grants: `acl`, `acl_membership`, `secrets`, `secret_use_log`
- Scheduler: `scheduled_tasks`, `task_run_logs`
- Slack pane: `pane_sessions`
- Cost: `cost_log`, `auth_users` (cost-cap column only; authd owns identity)
- Network: `network_rules`

Migrations: `routd/migrations/`. routd opens NO sibling DB — cross-daemon
data arrives over HTTP (authd identity, runed session_log).

`/openapi.json` emits schema for: `routes`, `web_routes`, `acl`, `secrets`
(the REST-exposed subset; `acl_membership`/`network_rules` are MCP-only).

## Entry points

Binary: `routd/cmd/routd/main.go`. Listen: `:8080` (`LISTEN_ADDR` default).

Endpoints (`server.go`, `channels.go`):

- `POST /v1/messages` — inbound from adapters (`messages:write`)
- `POST /v1/outbound` — adapter passthrough (`messages:write`)
- `GET|PUT|POST|DELETE /v1/routes` — route CRUD
- `GET|PUT|DELETE /v1/web_routes` — web route CRUD
- `GET /v1/web_presence` — folder vhost info (get_web_presence REST twin)
- `POST /v1/route_tokens/{chat,hook}` — mint chat/hook route tokens
- `GET /v1/route_tokens`, `DELETE /v1/route_tokens/{jid}` — list/revoke
- `POST /v1/route_tokens/resolve` — validate token
- `POST /v1/channels/register`, `POST /v1/channels/deregister` — adapter registry
- `GET /v1/channels` — adapter list
- `GET /v1/messages/{inspect,thread,find}` — message reads
- `GET /v1/routing/{resolve,errored}` — routing info
- `GET|POST /v1/engagement` — engagement state
- `GET /v1/sessions` — session info
- `GET /v1/users/{sub}/scopes` — login-time scope snapshot (`grants:read`)
- `POST /v1/acl`, `DELETE /v1/acl` — acl grant/revoke (`acl:write`)
- `POST /v1/secrets`, `DELETE /v1/secrets/{key}` — secret write/delete (`secrets:write`)
- `POST /v1/pane` — Slack pane context (`messages:write`)
- `GET /v1/tasks/due` — claim due tasks (`tasks:read`)
- `POST /v1/tasks/runlog`, `POST /v1/tasks/{id}/reschedule` — task logs/reschedule (`tasks:write`)
- `POST /v1/cost` — cost log write
- `POST /v1/turns/{turn_id}/{reply,send,document,history,like,edit,delete,pin,unpin,post,forward,quote,repost,send_voice,result}` — agent tool forwards
- `GET /openapi.json` — OpenAPI 3.1 doc (public, pre-auth)
- `GET /health` — healthcheck (public)

## Dependencies

- `auth` (offline token verify via authd JWKS), `chanreg` (channel
  registry + Deliverer), `ipc` (`ServeMCP`), `grants`, `groupfolder`,
  `obs`, `resreg`, `types`
- `authd` (JWKS), `runed` (`POST /v1/runs`), channel adapters (ingress/egress)

## Configuration

Core vars:

- `DATA_DIR` — `routd.db` + groups/web/tts dirs
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
- `ENGAGEMENT_TTL` — reply-state engagement window (default 30m)
- `SESSION_IDLE_EXPIRY` — session reset threshold (default 2d)
- `ARIZUKO_DEFAULT_MODEL` — fallback when group has none (default "claude-opus-4-8")
- `COST_CAPS_ENABLED` — pre-spawn budget gate (default "true")
- `MAX_TURN_RETRY` — SIGKILL/OOM/timeout retry ceiling (default 3)
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
- `AUDIT_ENABLED` — audit-system.jl emission (default "false")

## Health signal

`GET /health` returns 200 once the process is up. Red flag: turns stuck
unclaimed in the loop, or runed unreachable on dispatch.

## Observability

slog → stderr (JSON) + journald. OTLP export when `OTEL_EXPORTER_OTLP_ENDPOINT`
is set (see `obs/`, `specs/5/O-otlp-export.md`). Every slog event carrying
`turn_id` gets a deterministic TraceID so the collector groups one turn's span.

Audit events (mutating MCP tools) → `audit-system.jl` when `AUDIT_ENABLED=true`.

## Files

- `cmd/routd/main.go` — daemon wiring, config, verifier, loop, channel registry
- `server.go` — HTTP mux + auth gates (`Handler`, `authz`)
- `channels.go` — adapter register/deregister + channel health loop
- `loop.go` — orchestration loop (poll/claim/dispatch)
- `dispatch.go` — turn dispatch to runed + enrichment + budget gate
- `turns.go` — turn callbacks (`/v1/turns/{turn_id}/*`) + agent tool forwards
- `deliver.go` — channel Deliverer (egress fanout via chanreg)
- `mcp.go` — in-process per-turn MCP socket (`ServeTurnMCP`, `buildGatedFns`)
- `prompt.go`, `prompt_db.go` — prompt envelope build (observe window, persona)
- `proactive.go`, `steer.go` — proactive interjection + slash-command dispatch
- `routes_http.go`, `tokens_http.go`, `web_routes_http.go`, `reads_http.go` — REST CRUD
- `db.go` — `routd.db` writes (PutMessage, turn claims, routing)
- `reads.go` — `routd.db` reads (messages, chats, sessions)
- `session.go`, `identity.go`, `onbod.go` — federation to runed/authd/onbod
- `connectors.go` — MCP connector catalog loader
- `enrich.go` — inbound media download + Whisper transcription
- `tts.go` — send_voice synthesis via TTS service
- `budget.go`, `network.go`, `tokens.go`, `spawn.go` — helpers

## Status

Live — the split is the only topology (gated removed, v0.50.0). Spec: `specs/5/E`.

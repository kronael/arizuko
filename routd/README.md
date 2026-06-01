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

`routd.db` (inherits gated's schema authority for the conversation
tables): `groups`, `chats`, `messages`, `routes`, `sessions`,
`turn_context`, `turn_results`, `cost_log`, `web_routes`, `route_tokens`,
`network_rules`, plus supporting state. Migrations in `routd/migrations/`.
`/openapi.json` exposes the cold-tier config resources: `groups`,
`routes`, `web_routes`, `acl`, `acl_membership`, `secrets`,
`network_rules`.

## Entry points

- Binary: `routd/cmd/routd/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface highlights (`server.go`,
  `channels.go`):
  - `POST /v1/messages`, `POST /v1/outbound` — inbound append / outbound
  - `GET|PUT|POST|DELETE /v1/routes`, `/v1/web_routes`, `/v1/route_tokens/*`
  - `POST /v1/channels/register|deregister`, `GET /v1/channels` — adapter registry
  - `GET /v1/messages/{inspect,thread,find}`, `/v1/routing/*`, `/v1/sessions`, `/v1/cost`
  - `POST /v1/turns/{turn_id}/{reply,send,document,like,edit,delete,pin,unpin,result}` — agent tool forwards
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (offline token verify via authd JWKS), `chanreg` (channel
  registry + Deliverer), `ipc` (`ServeMCP`), `grants`, `groupfolder`,
  `obs`, `resreg`, `types`
- `authd` (JWKS), `runed` (`POST /v1/runs`), channel adapters (ingress/egress)

## Configuration

- `DATA_DIR` — `routd.db` + groups/web dirs
- `AUTHD_URL` — JWKS source; unset → verifier open (local-dev)
- `AUTHD_SERVICE_KEY` / `ROUTD_SERVICE_TOKEN` — service identity for outbound
- `RUNED_URL` (default `http://runed:8080`), `WEB_HOST`, `CHANNEL_SECRET`
- `LISTEN_ADDR`, `ENGAGEMENT_TTL`, `ASSISTANT_NAME`, `SEND_DISABLED_CHANNELS`

## Health signal

`GET /health` returns 200 once the process is up. Red flag: turns stuck
unclaimed in the loop, or runed unreachable on dispatch.

## Files

- `cmd/routd/main.go` — daemon wiring, verifier, loop, channel registry
- `server.go` — HTTP surface; `channels.go` — adapter register/deregister
- `loop.go`, `dispatch.go`, `turns.go`, `deliver.go` — orchestration + egress
- `mcp.go` — in-process per-turn MCP socket (`ServeTurnMCP`)
- `prompt.go`, `prompt_db.go`, `proactive.go`, `steer.go` — prompt build + steering
- `*_http.go` — REST adapters over routes / tokens / reads / web_routes
- `db.go`, `reads.go` — `routd.db` persistence

## Status

Built and tested in-tree, NOT yet deployed (behind `CUTOVER_SPLIT`);
`gated` is the live monolith. Spec: `specs/5/E`.

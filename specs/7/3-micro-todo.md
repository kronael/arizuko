# Microservice port — TODO

Architecture spec: `specs/7/2-micro-architecture.md`
Channel protocol: `specs/7/6-channel-protocol.md`

## Phase 1: Router HTTP API

- [ ] Add channel registration endpoint (POST /v1/channels/register)
- [ ] Add inbound message endpoint (POST /v1/messages)
- [ ] Add chat metadata endpoint (POST /v1/chats)
- [ ] Add deregister endpoint (POST /v1/channels/deregister)
- [ ] Channel registry: in-memory map of registered channels
- [ ] Route outbound to registered channel via HTTP POST /send
- [ ] Health check loop: ping channel /health every 30s
- [ ] Auto-deregister on 3 consecutive health failures
- [ ] Queue outbound when channel is down, replay on re-register
- [ ] Auth: shared secret for registration, session tokens

## Phase 2: First external channel (telegram)

- [ ] Extract telegram adapter into standalone binary
- [ ] Implements: HTTP server (/send, /send-file, /typing, /health)
- [ ] Implements: HTTP client (register, deliver messages, chat metadata)
- [ ] Connects to telegram API on one side, router HTTP on the other
- [ ] Test: run adapter standalone, send/receive messages
- [ ] Retire in-process telegram channel code

## Phase 3: Process runner (docker compose)

- [ ] Monorepo layout: each channel in channels/<name>/ with Dockerfile
- [ ] `make` builds all images (router + channel adapters)
- [ ] `./arizuko` generates docker-compose.yml from services/\*.toml
- [ ] `docker compose up -d` manages lifecycle
- [ ] `arizuko status` shows router + registered channels
- [ ] Extension support: drop .toml in services/ for third-party images

## Phase 4: MCP IPC (replace file-based)

- [ ] Router becomes MCP server on unix socket per group
- [ ] Agent containers connect as MCP clients via socat bridge
- [ ] Tools: send_message, send_file, schedule_task, etc.
- [ ] Bidirectional: router pushes notifications to agent
- [ ] Remove file-based IPC (requests/, replies/, messages/)
- [ ] Remove SIGUSR1 signaling

## Phase 5: Remaining channels

- [ ] Discord adapter (standalone binary)
- [ ] WhatsApp adapter (standalone binary)
- [ ] Email adapter (standalone binary)

## Phase 6: Web extraction (if needed)

- [ ] Separate web server process (or keep router-internal)
- [ ] If separate: talks to router via HTTP like channels
- [ ] Auth, slink, vite proxy

## Open

- Extension packaging: how to distribute/install adapters
- Event types beyond messages (reactions, edits, joins)
- Large file delivery (base64 vs upload endpoint)

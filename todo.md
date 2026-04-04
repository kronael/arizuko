# TODO

## memory

- collapse `sessions` table into `groups.session_id` column (see specs/1/7-db-bootstrap.md)
- test SDK resume failure: send bad session ID to container, observe whether SDK throws / errors / silently starts fresh — record result in specs/1/P-memory-session.md open item 1

- rename product: cheerleader → evangelist, evangelist → support
- v3: HTTP request scrubbing (strip secrets from outbound agent HTTP calls)

## v2 channels

- email channel (IMAP + SMTP) — specs/1/8-email.md
- reddit channel (DMs via snoowrap) — specs/3/G-reddit.md
- facebook channel (fca-unofficial) — specs/3/7-facebook.md
- twitter channel (agent-twitter-client) — specs/3/L-twitter.md

## feed adapter (phase 1, all feed channels)

- synthetic inbound: dm / mention / timeline_post / reply_to_us event types
- outbound: reply / repost / react / post action types
- per-adapter watch config (accounts, keywords, subreddits)

## phase 2 (defer)

- MCP tools for deep querying: browse threads, search, follow, trending
- bus question: study HTTP proxying + MCP HTTP vs message bus before speccing

## microservice port

Architecture spec: `specs/7/0-architecture.md`
Channel protocol: `specs/7/1-channel-protocol.md`

### Phase 1: Router HTTP API ✓

- [x] Add channel registration endpoint (POST /v1/channels/register)
- [x] Add inbound message endpoint (POST /v1/messages)
- [x] Add chat metadata endpoint (POST /v1/chats)
- [x] Add deregister endpoint (POST /v1/channels/deregister)
- [x] Channel registry: in-memory map of registered channels
- [x] Route outbound to registered channel via HTTP POST /send
- [x] Health check loop: ping channel /health every 30s
- [x] Auto-deregister on 3 consecutive health failures
- [x] Queue outbound when channel is down, replay on re-register
- [x] Auth: shared secret for registration, session tokens

### Phase 2: First external channel (telegram) ✓

- [x] Extract telegram adapter into standalone binary
- [x] Implements: HTTP server (/send, /send-file, /typing, /health)
- [x] Implements: HTTP client (register, deliver messages, chat metadata)
- [x] Connects to telegram API on one side, router HTTP on the other
- [x] Test: run adapter standalone, send/receive messages
- [x] Retire in-process channel code (all channels removed)

### Phase 3: Process runner (docker compose) ✓

- [x] Monorepo layout: each channel in channels/<name>/ with Dockerfile
- [x] `make` builds all images (router + channel adapters)
- [x] `arizuko compose` generates docker-compose.yml from services/\*.toml
- [x] `docker compose up -d` manages lifecycle
- [x] `arizuko status` shows router + registered channels
- [x] Extension support: drop .toml in services/ for third-party images

### Phase 4: MCP IPC (replace file-based)

- [ ] Router becomes MCP server on unix socket per group
- [ ] Agent containers connect as MCP clients via socat bridge
- [ ] Tools: send_message, send_file, schedule_task, etc.
- [ ] Bidirectional: router pushes notifications to agent
- [ ] Remove file-based IPC (requests/, replies/, messages/)
- [ ] Remove SIGUSR1 signaling

### Phase 5: Remaining channels

- [ ] Discord adapter (standalone binary)
- [ ] WhatsApp adapter (standalone binary)
- [ ] Email adapter (standalone binary)

### Phase 6: Web extraction (if needed)

- [ ] Separate web server process (or keep router-internal)
- [ ] If separate: talks to router via HTTP like channels
- [ ] Auth, slink, vite proxy

### Open

- Extension packaging: how to distribute/install adapters
- Event types beyond messages (reactions, edits, joins)
- Large file delivery (base64 vs upload endpoint)

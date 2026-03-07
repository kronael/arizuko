# Microservice port — TODO

Architecture spec: `specs/v2/micro/architecture.md`

## Phase 1: Outbox pattern (minimal change)

- [ ] Add `outbox` table to store schema
- [ ] Gateway writes outbox rows instead of calling channel directly
- [ ] Channel adapters poll outbox, send, mark sent_at
- [ ] Enforce no-cross-imports between channel/gateway/web

This gives the clean separation without operational complexity.
Everything stays in one process.

## Phase 2: Channel extraction

- [ ] Extract each channel adapter into standalone process
  - [ ] Telegram adapter (TS — grammy)
  - [ ] Discord adapter (TS — discord.js)
  - [ ] WhatsApp adapter (TS — baileys)
  - [ ] Email adapter (Go or Python — go-imap / aioimaplib)
- [ ] Each adapter: read platform API → INSERT messages,
      SELECT outbox → send via platform API
- [ ] No imports from gateway code
- [ ] Test: run adapter standalone, verify rows appear in DB

## Phase 3: MCP IPC (replace file-based)

- [ ] Gateway becomes MCP server on unix socket per group
- [ ] Agent containers connect as MCP clients via socat bridge
- [ ] Tools: send_message, send_file, schedule_task, etc.
- [ ] Bidirectional: gateway pushes notifications to agent
- [ ] Remove file-based IPC (requests/, replies/, messages/)
- [ ] Remove SIGUSR1 signaling

## Phase 4: Process management

- [ ] systemd template units (@.service, @.target)
- [ ] `arizuko create` generates and enables units
- [ ] `arizuko provision` diffs .env → add/remove channel units
- [ ] PartOf= for target stop, After= for startup ordering
- [ ] Per-instance targets: arizuko@<name>.target

## Phase 5: Scheduler extraction

- [ ] Scheduler as separate process (or keep gateway-internal)
- [ ] Reads tasks table, inserts synthetic messages
- [ ] Multiple schedulers work naturally (different origin IDs)

## Phase 6: Web server extraction

- [ ] Separate HTTP server process
- [ ] Auth, share links, vite proxy
- [ ] Writes messages table for slink inbound

## Open design questions

### Notification mechanism

How do processes learn about new rows?

- Poll every 100ms (simple, slight latency)
- inotify on WAL file (instant but brittle)
- Unix socket notify ping (gateway sends "new outbox")
- Poll is probably fine for chat latency

### SQLite contention

Multiple writers compete for write lock under WAL.
busy_timeout=5000 helps but may not be enough under load.
Monitor SQLITE_BUSY errors in production.

### Schema versioning

All processes share one schema. How to evolve?

- Migration table with version number
- Each process checks schema version at startup
- Incompatible changes require coordinated deploy

### Agent container IPC transport

MCP SDK supports stdio, sse, http — not unix sockets natively.
Solution: socat stdio-to-socket bridge. One-liner in MCP config:

```json
{
  "nanoclaw": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/gateway.sock"]
  }
}
```

### Extension packaging

Rough idea for installable extensions (channels, MCP servers, etc):

- GitHub repo with known structure (entrypoint, config, schema)
- `arizuko install <repo>` clones and wires in
- Not designed yet — lots unclear about security, versioning

# ROADMAP

Specs live in `specs/<version>/`. See TODO.md for task-level tracking.

## v1 — shipped

Functional multi-channel agent gateway.

- 5 channels: Telegram, WhatsApp, Discord, Email, Web/Slink
- Containerized Claude Code agents (docker, stdin/stdout JSON)
- Group routing + delegation (5 rule types, depth limit)
- Skills system (diary, migrate, facts researcher)
- Voice transcription (whisper sidecar)
- File I/O (send/receive documents, images, audio, video)
- Per-channel output styles (telegram, discord, email, web)
- Action registry (Zod schemas, authorization)
- IPC: signal-driven file protocol (SIGUSR1 wake)
- Scheduled tasks (cron)
- Auth (argon2 + JWT)
- Session continuity (diary injection on new session)
- JID normalization (telegram:, whatsapp:, discord:, email:, web:)
- Mount security (allowlist outside project dir)

## v1m1 — next to ship

Partial work, close to done. `specs/v1m1/`

- memory-facts: gateway injection + MCP tools
- paths: hostPath() elimination
- autotesting: IPC drain + voice roundtrip gaps
- sse: per-sender scoping
- worlds-rooms: phase 2 threads/rooms
- session-recovery: recovery notes on eviction
- work: /work skill, staleness detection

## v1m2 — outstanding, not urgent

`specs/v1m2/`

- message-mcp: get_history/get_thread MCP tools
- agent-messaging: structured agent-to-agent messages
- identities: cross-channel identity linking
- message-wal: pending_delivery table, ack protocol

## v2m1 — memory layers, agent teams, feed adapters

New subsystems and channel types. `specs/v2m1/`

- memory-episodic: diary aggregation to weekly/monthly
- agent-teams: multi-agent collaboration
- agent-pipeline: continuation payload, multi-hop routing
- ipc-mcp-proxy: unix socket, replace file IPC
- plugins: dynamic channel/feature loading
- workflows: multi-step workflow primitives
- feed adapters: reddit, twitter, facebook, gmail, webdav

## v2m2 — Go rewrite

Gateway rewrite in Go. Agent container stays TypeScript. `specs/v2m2/`

- Single static binary, no node_modules
- Native concurrency (goroutines vs polling loops)
- Same SQLite schema, same .env, same IPC protocol
- Drop-in replacement for TS gateway

## Atlas

Product-specific roadmap. `specs/atlas/`

- User context: per-user memory, gateway injection
- Semantic search: embeddings MCP sidecar
- Gateway fact injection (top-N relevant)
- v2 sandboxed support: frontend/backend agent split

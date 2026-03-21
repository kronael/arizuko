# specs/4 — shipped architecture

Authoritative for the shipped Go codebase. All specs here describe
implemented, running code.

## Core

- [0-architecture.md](0-architecture.md) — service topology, shared DB, migrations, paths
- [9-gated.md](9-gated.md) — gateway daemon: loop, routing, templates, commands, output
- [10-ipc.md](10-ipc.md) — MCP server: 16 tools inline, identity from socket path
- [11-auth.md](11-auth.md) — authorization policy, tiers, web auth, OAuth, JWT
- [8-scheduler-service.md](8-scheduler-service.md) — timed daemon: cron poll, one-shot, schema
- [1-channel-protocol.md](1-channel-protocol.md) — HTTP adapter protocol: register/send/health
- [19-action-grants.md](19-action-grants.md) — grant rules: tier defaults, DB overrides, delegation
- [21-onboarding.md](21-onboarding.md) — unrouted JID flow: name, approve, create world (onbod)
- [23-topic-routing.md](23-topic-routing.md) — @agent delegation, #topic sessions, prefix routes
- [26-prototypes.md](26-prototypes.md) — prototype/ dirs for child spawning, lifecycle

## Features

- [13-message-ids.md](13-message-ids.md) — reply/forward metadata, channel coverage tables
- [15-code-research.md](15-code-research.md) — code researcher agent, SYSTEM.md template
- [17-knowledge-system.md](17-knowledge-system.md) — memory layer taxonomy, push vs pull, /recall
- [18-web-vhosts.md](18-web-vhosts.md) — hostname-based routing, vhosts.json
- [24-recall.md](24-recall.md) — knowledge retrieval: v1 LLM grep (shipped), v2 FTS5+sqlite-vec (planned)
- [social-adapters.md](social-adapters.md) — teled, discd, emaid, bskyd, mastd, reditd
- [task-logs.md](task-logs.md) — task_run_logs table: schema and timed population

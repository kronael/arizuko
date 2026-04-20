---
status: deferred
---

# specs/4 — dashboards, memory, products

Mixed bag. Most specs describe shipped core architecture; the rest
are deferred.

## Shipped

- [1-channel-protocol.md](1-channel-protocol.md) — HTTP adapter protocol: register/send/health
- [8-scheduler-service.md](8-scheduler-service.md) — timed daemon: cron poll, one-shot, schema
- [9-gated.md](9-gated.md) — gateway daemon: loop, routing, templates, commands, output
- [10-ipc.md](10-ipc.md) — MCP server: 20 tools inline, identity from socket path
- [11-auth.md](11-auth.md) — authorization policy, tiers, web auth, OAuth, JWT
- [13-message-ids.md](13-message-ids.md) — reply/forward metadata, channel coverage tables
- [15-code-research.md](15-code-research.md) — code researcher agent, SYSTEM.md template
- [17-knowledge-system.md](17-knowledge-system.md) — memory layer taxonomy, push vs pull
- [18-web-vhosts.md](18-web-vhosts.md) — hostname-based routing, vhosts.json
- [19-action-grants.md](19-action-grants.md) — grant rules, tier defaults, delegation
- [23-topic-routing.md](23-topic-routing.md) — `@agent` delegation, `#topic` sessions
- [26-prototypes.md](26-prototypes.md) — spawn/copy + TTL cleanup via timed
- [Y-minimal-setup.md](Y-minimal-setup.md) — PROFILE-gated compose generation
- [chanlib-refactor.md](chanlib-refactor.md) — adapter boilerplate moved to chanlib
- [social-adapters.md](social-adapters.md) — teled, discd, emaid, bskyd, mastd, reditd, whapd
- [task-logs.md](task-logs.md) — task_run_logs schema
- [P-personas.md](P-personas.md) — versioning, image distribution, persona files

## Partial

- [24-recall.md](24-recall.md) — v1 LLM grep shipped; v2 FTS5+sqlite-vec planned
- [Q-dash-memory.md](Q-dash-memory.md) — view shipped; edit endpoints open
- [X-extensions.md](X-extensions.md) — skills shipped; marketplace deferred

## Unshipped

- [G-instance-repos.md](G-instance-repos.md) — instance configs as git repos (`--from`)
- [e2e-tests.md](e2e-tests.md) — mock-agent-based end-to-end tests
- [products.md](products.md) — curated persona+skill templates (`ant/products/<name>/`)

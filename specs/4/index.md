---
status: shipped
---

# specs/4 — dashboards, memory, scheduler, personas

## Messaging

- [social-adapters.md](social-adapters.md) — adapter roster; chanlib ownership; whapd/emaid decisions
- [13-message-ids.md](13-message-ids.md) — reply threading, channel coverage, forward-metadata non-goal
- [23-topic-routing.md](23-topic-routing.md) — `@agent` delegation, `#topic` sessions
- [26-prototypes.md](26-prototypes.md) — `prototype/` spawn, `max_children`, folder naming

## Web surfaces

- [2-proxyd.md](2-proxyd.md) — public-facing auth proxy; route order; fail-closed rule
- [3-chat-ui.md](3-chat-ui.md) — webd: chat widget, JID model, auth planes, `/me/*` portal
- [Q-dash-memory.md](Q-dash-memory.md) — dashd memory browser + path allow-list
- [V-dashd-acl-ui.md](V-dashd-acl-ui.md) — dashd operator ACL editor on the unified `acl` table

## Authorization

- [19-action-grants.md](19-action-grants.md) — **superseded in part** — rule grammar survives; storage → [`../5/32`](../5/32-acl-unified.md), tiers → [`../5/33`](../5/33-paths-roles.md)
- [11-auth.md](11-auth.md) — outbound JID authorization (subtree containment, no tier bypass)

## Runtime

- [8-scheduler-service.md](8-scheduler-service.md) — timed: cron poll, schema, `task_run_logs`
- [10-ipc.md](10-ipc.md) — per-group MCP socket; identity as parameter; spawn + route guards
- [17-knowledge-system.md](17-knowledge-system.md) — memory layers, push vs pull, recall, compression
- [P-personas.md](P-personas.md) — versioning, image distribution, the three voice layers
- [S-e2e-tests.md](S-e2e-tests.md) — per-daemon boundary tests; why not docker e2e

## Retired

- [9-gated.md](9-gated.md) — **superseded** — gated split into `5/E-routd`, `5/P-runed`, `5/1-auth-standalone`

Merged or moved out of this phase:

| Was                     | Now                                                                                               |
| ----------------------- | ------------------------------------------------------------------------------------------------- |
| `18-web-vhosts.md`      | [`../5/V-web-vhosts.md`](../5/V-web-vhosts.md) (2026-05-26)                                       |
| `chanlib-refactor.md`   | [`social-adapters.md`](social-adapters.md)                                                        |
| `task-logs.md`          | [`8-scheduler-service.md`](8-scheduler-service.md)                                                |
| `24-recall.md`          | [`17-knowledge-system.md`](17-knowledge-system.md)                                                |
| `15-code-research.md`   | [`17-knowledge-system.md`](17-knowledge-system.md) + [`../5/21-products.md`](../5/21-products.md) |
| `U-user-dashboard.md`   | [`3-chat-ui.md`](3-chat-ui.md)                                                                    |
| `1-channel-protocol.md` | [`../5/34-channel-protocol.md`](../5/34-channel-protocol.md) (2026-08-02)                         |
| `9-acl-unified.md`      | [`../5/32-acl-unified.md`](../5/32-acl-unified.md) (2026-08-02)                                   |
| `R-paths-roles.md`      | [`../5/33-paths-roles.md`](../5/33-paths-roles.md) (2026-08-02)                                   |
| `Y-minimal-setup.md`    | [`../5/27-compose-native-packaging.md`](../5/27-compose-native-packaging.md)                      |

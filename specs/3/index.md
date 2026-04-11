---
status: active
---

# specs/3 — features & extensions

30 specs. Originally 25; 9 removed (shipped and self-evident), 14 added.

## Shipped (trimmed to design decisions)

- [5-permissions.md](5-permissions.md) — escalation protocol, mount enforcement, delegation format
- [E-memory-session.md](E-memory-session.md) — session switching triggers, context injection, /new
- [H-jid-format.md](H-jid-format.md) — context injection design, clock header, message XML attrs
- [J-container-commands.md](J-container-commands.md) — two-path concept, command column
- [L-chat-bound-sessions.md](L-chat-bound-sessions.md) — IPC encoding, delivery guarantees, parallelism
- [P-message-ids.md](P-message-ids.md) — channel coverage tables, WhatsApp limitation, XML format
- [E-message-scoping.md](E-message-scoping.md) — impulse_config on routes, per-JID message gating

## Shipped (kept as-is)

- [0-agent-capabilities.md](0-agent-capabilities.md) — container tooling inventory, media flow
- [7-user-context.md](7-user-context.md) — per-user memory files, gateway injection
- [D-knowledge-system.md](D-knowledge-system.md) — push vs pull layers, injection pattern
- [c-audit-log.md](c-audit-log.md) — StoreOutbound + wiring in gateway/ipc
- [Y-thread-routing.md](Y-thread-routing.md) — persist reply ID, thread mapping in adapters, Channel.Send threadID
- [Z-reply-routing.md](Z-reply-routing.md) — routed_to column, reply-chain group resolution
- [a-sticky-routing.md](a-sticky-routing.md) — @group and #topic commands, 38 test cases
- [8-web-virtual-hosts.md](8-web-virtual-hosts.md) — vhosts.json hostname→world routing in proxyd

## Shipped (partial — minor gaps)

- [b-control-chat.md](b-control-chat.md) — root control chat; gap: /status command, /approve /reject wiring
- [d-dashboards.md](d-dashboards.md) — dashd with 6 dashboards; gap: banner health, expandable detail

## Superseded

- [4-dashboards.md](4-dashboards.md) — operator dashboard architecture (superseded by d-dashboards.md)
- [Q-dash-status.md](Q-dash-status.md) — status dashboard (superseded by d-dashboards.md, dashd has status page)

## Planned

- [1-atlas.md](1-atlas.md) — sandboxed support hierarchy
- [3-support.md](3-support.md) — code researcher product pattern
- [6-session-recovery.md](6-session-recovery.md) — recovery note injection on abnormal end
- [B-memory-episodic.md](B-memory-episodic.md) — time-hierarchy diary aggregation (v2, many open questions)
- [F-memory-facts.md](F-memory-facts.md) — concept-centric knowledge (v2, depends on atlas)
- [R-researcher.md](R-researcher.md) — background research task pattern
- [V-platform-permissions.md](V-platform-permissions.md) — platform_grants table
- [W-work.md](W-work.md) — ephemeral active-task file
- [X-worlds-rooms.md](X-worlds-rooms.md) — room model research, comparative analysis
- [l-linkedin.md](l-linkedin.md) — LinkedIn channel adapter
- [G-history-backfill.md](G-history-backfill.md) — adapter history backfill on startup (WhatsApp excepted)
- [e-migration-announce.md](e-migration-announce.md) — paired `.md` on migrations auto-fans out upgrade notes to active groups

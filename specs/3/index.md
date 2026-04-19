---
status: active
---

# specs/3 — features & extensions

| Spec                                                   | Status    | Hook                                                                |
| ------------------------------------------------------ | --------- | ------------------------------------------------------------------- |
| [0-agent-capabilities.md](0-agent-capabilities.md)     | shipped   | container tooling inventory, media flow                             |
| [1-atlas.md](1-atlas.md)                               | partial   | facts shipped; escalation response protocol wiring open             |
| [3-support.md](3-support.md)                           | unshipped | code-researcher product config                                      |
| [5-permissions.md](5-permissions.md)                   | shipped   | four-tier model, mount enforcement, action authorization            |
| [7-user-context.md](7-user-context.md)                 | shipped   | per-user memory files, gateway injects identity tag                 |
| [8-web-virtual-hosts.md](8-web-virtual-hosts.md)       | shipped   | one DNS hostname per world, `web_host` column                       |
| [B-memory-episodic.md](B-memory-episodic.md)           | deferred  | time-hierarchy diary aggregation (v2)                               |
| [D-knowledge-system.md](D-knowledge-system.md)         | partial   | push vs pull layers, injection XML                                  |
| [E-memory-session.md](E-memory-session.md)             | shipped   | session switching, 2-day idle expiry, context injection on reset    |
| [E-message-scoping.md](E-message-scoping.md)           | shipped   | impulse as universal trigger gate, per-route config, DENY access    |
| [F-memory-facts.md](F-memory-facts.md)                 | deferred  | concept-centric knowledge (depends on atlas)                        |
| [G-history-backfill.md](G-history-backfill.md)         | shipped   | fetch_history MCP tool across 7 adapters (whapd excepted)           |
| [H-jid-format.md](H-jid-format.md)                     | shipped   | clock header + message XML attrs + context block                    |
| [J-container-commands.md](J-container-commands.md)     | shipped   | agent vs raw container paths, `command` column on tasks             |
| [L-chat-bound-sessions.md](L-chat-bound-sessions.md)   | shipped   | IPC encoding, delivery guarantees, cross-folder parallelism         |
| [P-message-ids.md](P-message-ids.md)                   | partial   | reply/forward metadata; WhatsApp reply deferred                     |
| [R-researcher.md](R-researcher.md)                     | unshipped | background research subagent writing to facts/                      |
| [V-platform-permissions.md](V-platform-permissions.md) | unshipped | `platform_grants` table parallel to routes                          |
| [W-work.md](W-work.md)                                 | unshipped | ephemeral work.md state file, agent-managed                         |
| [X-worlds-rooms.md](X-worlds-rooms.md)                 | unshipped | room model research vs brainpro/muaddib/ElizaOS                     |
| [Y-thread-routing.md](Y-thread-routing.md)             | shipped   | persist last-reply-id, Topic mapping, `routed_to` on messages       |
| [Z-reply-routing.md](Z-reply-routing.md)               | shipped   | per-sender batching, chunk chaining, escalation reply threading     |
| [a-sticky-routing.md](a-sticky-routing.md)             | shipped   | `@group` / `#topic` commands, sticky columns on chats               |
| [b-control-chat.md](b-control-chat.md)                 | partial   | root group as control chat; `/status` and approve wiring pending    |
| [c-audit-log.md](c-audit-log.md)                       | shipped   | `PutMessage` unified path, `source` column semantics                |
| [d-dashboards.md](d-dashboards.md)                     | partial   | dashd + six dashboards; banner health and expandable detail pending |
| [e-migration-announce.md](e-migration-announce.md)     | unshipped | paired `.md` on migrations fans out upgrade notes to active groups  |
| [l-linkedin.md](l-linkedin.md)                         | shipped   | LinkedIn channel adapter (`linkd`)                                  |

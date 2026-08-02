---
status: shipped
---

# specs/3 — features & extensions

| Spec                                                 | Status             | Hook                                                            |
| ---------------------------------------------------- | ------------------ | --------------------------------------------------------------- |
| [0-agent-capabilities.md](0-agent-capabilities.md)   | shipped            | container tooling inventory, media flow                         |
| [1-atlas.md](1-atlas.md)                             | shipped            | facts corpus; why the public leaf can't research                |
| [7-user-context.md](7-user-context.md)               | shipped            | per-user memory files; identity tag, not content                |
| [E-memory-session.md](E-memory-session.md)           | shipped            | session switching; why 2-day idle expiry is a hallucination fix |
| [G-history-backfill.md](G-history-backfill.md)       | shipped            | `fetch_history` vs `inspect_messages` — two audiences           |
| [L-chat-bound-sessions.md](L-chat-bound-sessions.md) | shipped            | IPC encoding, delivery guarantees, cross-folder parallelism     |
| [W-work.md](W-work.md)                               | shipped            | ephemeral work.md; the staleness nudge that never shipped       |
| [Y-thread-routing.md](Y-thread-routing.md)           | shipped            | persisted reply anchor, Topic as the one thread id              |
| [a-sticky-routing.md](a-sticky-routing.md)           | shipped            | `@group` / `#topic` commands, sticky columns on chats           |
| [b-control-chat.md](b-control-chat.md)               | shipped            | why there is _no_ control chat; notify is not wired             |
| [c-audit-log.md](c-audit-log.md)                     | shipped            | one row shape for in/out; what `source` is actually for         |
| [5-tool-authorization.md](5-tool-authorization.md)   | superseded-in-part | tier tables kept as the source for `5/33`'s role bundles        |
| [d-dashboards.md](d-dashboards.md)                   | superseded-in-part | tile model + HTMX conventions; live surface is `7/1`            |
| [l-linkedin.md](l-linkedin.md)                       | shipped            | LinkedIn adapter; N+1 polling and the auto-publish default      |

Merged or moved out of this phase:

| Was                         | Now                                                                                                                                                                                  |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `8-web-virtual-hosts.md`    | [`../5/V-web-vhosts.md`](../5/V-web-vhosts.md) — `web_host` column, `set_web_host` IPC and CLI all retired; per-world hosts are derived                                              |
| `V-platform-permissions.md` | [`../5/32-acl-unified.md`](../5/32-acl-unified.md) — the proposed `platform_grants` table was never built; permissions are routes-derived                                            |
| `D-knowledge-system.md`     | [`../4/17-knowledge-system.md`](../4/17-knowledge-system.md) — same pattern, stated once                                                                                             |
| `E-message-scoping.md`      | [`../5/B-route-mode-ingestion.md`](../5/B-route-mode-ingestion.md) — `routes.impulse_config` dropped in migration 0054; the surviving DENY-not-filter rule lives in `ipc/inspect.go` |
| `H-jid-format.md`           | [`../5/S-jid-format.md`](../5/S-jid-format.md)                                                                                                                                       |
| `J-container-commands.md`   | Deleted — never shipped. `container.Input` has no `Command` field and `scheduled_tasks` has no `command` column                                                                      |
| `Z-reply-routing.md`        | Deleted — it was a table of six done checkboxes; the surviving rules are in [`Y-thread-routing.md`](Y-thread-routing.md)                                                             |

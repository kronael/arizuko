---
status: shipped
---

# specs/1 — core gateway

The first phase: routing, channels, auth, memory, container/IPC, agent
extension. Everything here shipped; several files have since been
narrowed to the decision that outlived the implementation.

| Spec                                         | Status  | Hook                                                        |
| -------------------------------------------- | ------- | ----------------------------------------------------------- |
| [F-group-routing.md](F-group-routing.md)     | shipped | Why the routes table is flat; `key=glob` match language     |
| [e-worlds.md](e-worlds.md)                   | shipped | World = first folder segment; containment ≠ authority       |
| [R-prompt-format.md](R-prompt-format.md)     | shipped | ContainerInput contract; `submit_turn` deliver-or-fail      |
| [N-memory-messages.md](N-memory-messages.md) | shipped | Stdin XML envelope, 100-msg window, new-session injection   |
| [Y-system-messages.md](Y-system-messages.md) | shipped | `<system origin=... event=...>` piggyback queue             |
| [L-memory-diary.md](L-memory-diary.md)       | shipped | Two-layer (MEMORY.md + diary); why rollups aren't injected  |
| [M-memory-managed.md](M-memory-managed.md)   | shipped | Claude Code managed CLAUDE.md + MEMORY.md, 200-line cap     |
| [Q-mime.md](Q-mime.md)                       | shipped | Serial enrichment; why the rewrite is persisted             |
| [H-introspection.md](H-introspection.md)     | shipped | `.gateway-caps` TOML, `.whisper-language` contract          |
| [9-extend-agent.md](9-extend-agent.md)       | shipped | settings.json merge order; hooks hardcoded                  |
| [B-extend-skills.md](B-extend-skills.md)     | shipped | SKILL.md frontmatter, naming, migration semantics           |
| [f-auth-oauth.md](f-auth-oauth.md)           | shipped | Account linking + the seven-case collision table            |
| [Z2-slink-sdk.md](Z2-slink-sdk.md)           | shipped | `/assets/*` `embed.FS` hosting; why not `template/web/pub/` |

Merged or moved out of this phase:

| Was                   | Now                                                                                                                                                                   |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `0-actions.md`        | Deleted — the action table drifts by construction. Tool inventory: [`../4/10-ipc.md`](../4/10-ipc.md); routing vocabulary: [`F-group-routing.md`](F-group-routing.md) |
| `8-email.md`          | [`../4/social-adapters.md`](../4/social-adapters.md) §emaid                                                                                                           |
| `a-task-scheduler.md` | [`../4/8-scheduler-service.md`](../4/8-scheduler-service.md) — it was a duplicate that said so itself                                                                 |
| `Z-slink-widget.md`   | [`../5/W-webhook-routes.md`](../5/W-webhook-routes.md) — `/slink/{token}/chat`, `/config` and CORS all moved to `/chat/<token>/`                                      |
| `W-slink.md`          | [`../5/W-webhook-routes.md`](../5/W-webhook-routes.md) — round-handle protocol; the stub outlived its last referrer (`14/3`, deleted 2026-08-02)                      |

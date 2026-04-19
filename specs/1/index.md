---
status: shipped
---

# specs/1 — core gateway

19 specs covering routing, channels, auth, memory, container/IPC,
agent extension.

| Spec                                             | Status    | Hook                                                      |
| ------------------------------------------------ | --------- | --------------------------------------------------------- |
| [0-actions.md](0-actions.md)                     | partial   | IPC/command action table; sidecar actions unimplemented   |
| [a-task-scheduler.md](a-task-scheduler.md)       | shipped   | Cron/interval/once schedules, isolated vs group context   |
| [8-email.md](8-email.md)                         | shipped   | `email:<thread_id>` JID; IMAP IDLE + SMTP reply threading |
| [W-slink.md](W-slink.md)                         | shipped   | Public 96-bit token; anon/auth/agent rate tiers           |
| [e-worlds.md](e-worlds.md)                       | shipped   | First folder segment = world; delegation boundary         |
| [F-group-routing.md](F-group-routing.md)         | shipped   | Flat routes table, match=key=glob, four-layer pipeline    |
| [R-prompt-format.md](R-prompt-format.md)         | shipped   | ContainerInput/Output JSON + sentinel markers             |
| [N-memory-messages.md](N-memory-messages.md)     | shipped   | Stdin XML envelope, 100-msg window, new-session injection |
| [Y-system-messages.md](Y-system-messages.md)     | shipped   | `<system origin=... event=...>` piggyback queue           |
| [L-memory-diary.md](L-memory-diary.md)           | shipped   | Two-layer (MEMORY.md + diary), YAML summary injection     |
| [M-memory-managed.md](M-memory-managed.md)       | shipped   | Claude Code managed CLAUDE.md + MEMORY.md, 200-line cap   |
| [V-sidecars.md](V-sidecars.md)                   | partial   | Gateway-managed sidecars shipped; agent-requested pending |
| [h-isolation.md](h-isolation.md)                 | shipped   | socat unix-socket bridge; gVisor/Firecracker future       |
| [Q-mime.md](Q-mime.md)                           | shipped   | Enricher pipeline, ContextAnnotation, Whisper integration |
| [H-introspection.md](H-introspection.md)         | shipped   | `.gateway-caps` TOML, `.whisper-language` contract        |
| [9-extend-agent.md](9-extend-agent.md)           | shipped   | settings.json merge order; hooks hardcoded                |
| [B-extend-skills.md](B-extend-skills.md)         | shipped   | SKILL.md frontmatter, naming, migration semantics         |
| [f-auth-oauth.md](f-auth-oauth.md)               | shipped   | JWT access + rotating refresh; Telegram/Discord/GH/Google |
| [S-reference-systems.md](S-reference-systems.md) | reference | brainpro/takopi/eliza-atlas adoption notes                |

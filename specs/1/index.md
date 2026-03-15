# specs/1 — v1 core (trimmed)

19 specs. Originally 37; 16 removed (TS-only, shipped and self-evident).
Remaining files trimmed to protocol contracts, design decisions, open questions.

## Actions & Tools

- [0-actions.md](0-actions.md) — tool table, routing rule types, sidecar actions
- [a-task-scheduler.md](a-task-scheduler.md) — schedule types, context modes, lifecycle state machine

## Channels

- [8-email.md](8-email.md) — JID format, threading table, SMTP reply contract
- [W-slink.md](W-slink.md) — token design, rate tiers, sender identity, MCP transport note

## Routing & Groups

- [e-worlds.md](e-worlds.md) — world boundaries, share mount, authorization scope
- [F-group-routing.md](F-group-routing.md) — delegation boundaries, error semantics, open questions

## Prompt & Format

- [R-prompt-format.md](R-prompt-format.md) — ContainerInput/Output JSON, sentinel markers, assembly order
- [N-memory-messages.md](N-memory-messages.md) — stdin XML envelope, injection rules, 100 message limit
- [Y-system-messages.md](Y-system-messages.md) — system message XML schema, origin table, flush semantics

## Memory

- [L-memory-diary.md](L-memory-diary.md) — two-layer model, YAML format, injection, nudge triggers
- [M-memory-managed.md](M-memory-managed.md) — 200-line limit, global CLAUDE.md, taxonomy

## Container & IPC

- [V-sidecars.md](V-sidecars.md) — socket paths, isolation modes, validation rules
- [h-isolation.md](h-isolation.md) — socat bridge, waitForSocket, gVisor/Firecracker future
- [Q-mime.md](Q-mime.md) — enricher pipeline, annotation format, media layout
- [H-introspection.md](H-introspection.md) — .gateway-caps TOML, .whisper-language contract

## Agent Extension

- [9-extend-agent.md](9-extend-agent.md) — settings.json merge order, hook limitation
- [B-extend-skills.md](B-extend-skills.md) — SKILL.md format, naming rules, migration semantics

## Auth

- [f-auth-oauth.md](f-auth-oauth.md) — token model, OAuth state, Telegram widget, PKCE matrix

## Reference

- [S-reference-systems.md](S-reference-systems.md) — brainpro, takopi, eliza-atlas analysis

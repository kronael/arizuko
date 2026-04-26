---
status: active
---

# specs/7 — grants, onboarding, access control

Self-service onboarding, glob-based ACL, scoped auth tokens,
pinned messages as context, local CLI interface, rate limits, cli chat,
dynamic channels, inspect tools, autocalls, tenant self-service.

- [25-pinned-messages.md](25-pinned-messages.md) `planned` — pinned messages as persistent agent context from chat
- [27-mass-onboarding.md](27-mass-onboarding.md) `shipped` — self-service onboarding, username=world, web auth gate
- [28-acl.md](28-acl.md) `shipped` — glob-matched user_groups, no operator/user distinction
- [28c-slink.md](28c-slink.md) `deferred` — slink scoped auth token (depends on 28-acl)
- [29-local-cli.md](29-local-cli.md) `unshipped` — local CLI for scripts/programs to send messages to groups
- [30-rate-limits.md](30-rate-limits.md) `unshipped` — usage_log table + per-group rate limits
- [31-cli-chat.md](31-cli-chat.md) `unshipped` — `arizuko chat` interactive terminal agent
- [32-dynamic-channels.md](32-dynamic-channels.md) `unshipped` — DB-backed channels, dashboard-managed creds, web pairing
- [33-inspect-tools.md](33-inspect-tools.md) `shipped` — inspect\_\* MCP family (messages, routing, tasks, session)
- [34-autocalls.md](34-autocalls.md) `shipped` — inline fact injection when schema cost > content cost
- [35-tenant-self-service.md](35-tenant-self-service.md) `shipped` — canonical org-chart model: invites, secrets, chats.kind, topic kinds, ops matrix

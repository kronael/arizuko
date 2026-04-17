---
status: active
---

# specs/8 — grants, onboarding, access control

5 specs. Self-service onboarding, glob-based ACL, scoped auth tokens,
pinned messages as context, local CLI interface.

- [25-pinned-messages.md](25-pinned-messages.md) `unshipped` — pinned messages as persistent agent context from chat
- [27-mass-onboarding.md](27-mass-onboarding.md) `shipped` — self-service onboarding, username=world, web auth gate
- [28-acl.md](28-acl.md) `shipped` — glob-matched user_groups, no operator/user distinction
- [28c-slink.md](28c-slink.md) `deferred` — slink scoped auth token (depends on 28-acl)
- [29-local-cli.md](29-local-cli.md) `unshipped` — local CLI for scripts/programs to send messages to groups

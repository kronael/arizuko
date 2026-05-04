---
status: active
---

# specs/8 — security + standalone

Hardening the security perimeter and splitting components into
standalone shippable units usable outside arizuko.

| Spec                                                     | Status               | Hook                                                          |
| -------------------------------------------------------- | -------------------- | ------------------------------------------------------------- |
| [7-self-learning.md](7-self-learning.md)                 | deferred             | Skill-guard PreToolUse hook (hermes peel)                     |
| [9-crackbox-standalone.md](9-crackbox-standalone.md)     | shipped              | egred — forward proxy with per-source allowlists (2026-04-29) |
| [10-crackbox-arizuko.md](10-crackbox-arizuko.md)         | shipped              | arizuko consumer of egred; sandd transition planned           |
| [11-crackbox-secrets.md](11-crackbox-secrets.md)         | draft                | egred-based secrets injection at egress                       |
| [12-crackbox-sandboxing.md](12-crackbox-sandboxing.md)   | shipped (2026-05-01) | crackbox `pkg/host/` library for KVM/qemu sandboxing          |
| [b-orthogonal-components.md](b-orthogonal-components.md) | planned              | Sibling shippable components: crackbox, gateway, mcp-firewall |
| [c-sandd.md](c-sandd.md)                                 | deferred             | Sandbox-spawn daemon; gated keeps spawn ownership for now     |

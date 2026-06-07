---
status: active
---

# specs/11 — genericization + publishing components

The home for arizuko's orthogonal, publishable, standalone
components and the sibling-component pattern they follow. Each
component builds, tests, ships, and runs without arizuko; arizuko
consumes it through a published surface (CLI / HTTP / `pkg/`),
never by reaching into its internals. The phase also covers the
security-hardening work that ships as such components (skill guard,
self-learning, the crackbox sandbox/egress family).

| Spec                                                     | Status               | Hook                                                                                                                                                                                                                                                             |
| -------------------------------------------------------- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [A-orthogonal-components.md](A-orthogonal-components.md) | draft                | The orthogonality PATTERN: sibling shippable components (crackbox, gateway, mcp-firewall) each build/test/ship/run without arizuko; zero arizuko-internal imports; consumed via CLI / HTTP / `pkg/`. `16`/`17`/`18` instantiate it. Moved from `5/A` 2026-05-29. |
| [7-self-learning.md](7-self-learning.md)                 | draft                | Pattern recognition → operator-gated proposals (skill, memory, persona)                                                                                                                                                                                          |
| [8-skill-guard.md](8-skill-guard.md)                     | draft                | Threat-pattern PreToolUse hook on agent-written skills (hermes peel)                                                                                                                                                                                             |
| [9-crackbox-standalone.md](9-crackbox-standalone.md)     | shipped              | egred — forward proxy with per-source allowlists (2026-04-29); arizuko's per-folder allowlist consumer                                                                                                                                                           |
| [12-crackbox-sandboxing.md](12-crackbox-sandboxing.md)   | shipped (2026-05-01) | crackbox `pkg/host/` library for KVM/qemu sandboxing; gated Docker→KVM backend transition                                                                                                                                                                        |
| [14-surrogate-oauth.md](14-surrogate-oauth.md)           | draft                | Surrogate OAuth dance + refresh wrapper — writer-side feed into 10/11's `secrets` table                                                                                                                                                                          |
| [15-crackbox-dns-filter.md](15-crackbox-dns-filter.md)   | draft                | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host`; ANY refused                                                                                                                                                                                       |
| [16-messaging-gateway.md](16-messaging-gateway.md)       | draft                | Generic message router over opaque ids; `routd` adds folder/grant domain on top                                                                                                                                                                                  |
| [17-mcp-firewall.md](17-mcp-firewall.md)                 | draft                | Transparent MCP proxy; deny-wins tool-call filter on flat ruleset; `mcpd` sits behind                                                                                                                                                                            |
| [18-openapi-mcp.md](18-openapi-mcp.md)                   | draft                | `x-mcp-*` annotation vocab + generic gateway; derives `5/5`'s MCP face from annotated REST                                                                                                                                                                       |
| [c-sandd.md](c-sandd.md)                                 | draft                | Sandbox-spawn daemon; gated keeps spawn ownership for now                                                                                                                                                                                                        |

The orthogonal-components pattern (the discipline this phase's
components follow) is [`A-orthogonal-components.md`](A-orthogonal-components.md) above.

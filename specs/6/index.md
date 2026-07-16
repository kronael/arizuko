---
status: active
---

# specs/6 — adoption & interop

The end goal is that people run arizuko instead of their own agent
harness. This phase is the path there: not "rip out your stack and
switch," but "wrap what you already run." Make runed's runtime
swappable so another harness runs inside an arizuko folder, import
its config/skills as-is, and let the folder coordinate (tenancy +
egress + ACL + web) be the layer above the harness. Campaign second,
on the proof the interop produces. The engine is an agentic
reimplementation loop — arizuko's self-learning, pointed outward.

## Specs

| Spec                                                             | Status    | Covers                                                                                                                                                                                                                                                            |
| ---------------------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [1-adoption-interop.md](1-adoption-interop.md)                   | draft     | interop-first strategy + the agentic reimplementation loop + the services a wrap provides + demand-class mode (absorbed `3`+`4`, 2026-07-16)                                                                                                                      |
| [2-target-matrix.md](2-target-matrix.md)                         | reference | competitive intel: which hub systems to incorporate (mechanisms) vs beat (products). Living Hub analysis, not a design spec                                                                                                                                       |
| [7-orthogonal-components.md](7-orthogonal-components.md)         | draft     | the sibling-component PATTERN: build/test/ship/run without arizuko; zero arizuko-internal imports; consumed via CLI/HTTP/`pkg/`. Anchors 8–13, applied by `16`. (was `12/A`)                                                                                      |
| [8-crackbox-standalone.md](8-crackbox-standalone.md)             | shipped   | egred — forward proxy with per-source allowlists; arizuko's per-folder egress consumer (was `12/9`)                                                                                                                                                               |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)             | shipped   | crackbox `pkg/host/` KVM/qemu sandbox library; Docker→KVM backend (was `12/12`)                                                                                                                                                                                   |
| [10-crackbox-dns-filter.md](10-crackbox-dns-filter.md)           | draft     | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host` (was `12/15`)                                                                                                                                                                                       |
| [11-messaging-gateway.md](11-messaging-gateway.md)               | draft     | generic message router over opaque ids; routd adds folder/grant on top (was `12/16`)                                                                                                                                                                              |
| [12-mcp-firewall.md](12-mcp-firewall.md)                         | draft     | transparent MCP proxy; deny-wins tool-call filter (was `12/17`)                                                                                                                                                                                                   |
| [13-sandd.md](13-sandd.md)                                       | draft     | sandbox-spawn daemon (was `12/c`)                                                                                                                                                                                                                                 |
| [16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md) | draft     | **the "ship in parts" catalogue**: two standalone axes (import-component / droppable-daemon) + the three-question method + package & daemon matrices + use-case clusters + the counterpart landscape & promote-vs-perfect strategy (absorbed `5`+`6`, 2026-07-16) |

`1`–`2` are the adoption/competitive narrative; `7`+`16` are the component
framing (the pattern + the catalogue); `8`–`13` are the concrete component
specs consolidated from the former phase 12 (2026-07-14) — `8`–`10` the
crackbox family (egress/sandbox/DNS), `11`–`13` the gateway/mcp-firewall/
sandd siblings. The self-learning + skill-guard features moved to `5/22`·
`5/23` (agent features, not shippable components, 2026-07-16).

## Ties

`specs/5/A` (positioning) · `USELESS.md` (the honest gaps this closes)
· the Agent Research Hub · `5/22` self-learning (the loop turned outward).

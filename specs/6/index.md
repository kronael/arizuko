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

| Spec                                                                 | Status  | Covers                                                                                                                                                          |
| -------------------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [1-adoption-interop.md](1-adoption-interop.md)                       | draft   | interop-first strategy, campaign, the agentic reimplementation loop                                                                                             |
| [2-target-matrix.md](2-target-matrix.md)                             | draft   | which hub systems to incorporate (mechanisms) vs beat (products)                                                                                                |
| [3-graph-taxonomy-answer.md](3-graph-taxonomy-answer.md)             | draft   | ride the graph/taxonomy hype the non-intrusive way (folder = substrate)                                                                                         |
| [4-harness-fleet-services.md](4-harness-fleet-services.md)           | draft   | worked example: arizuko as the service layer above N Hermeses — wrap vs reimplement-in-Go, keys/security up one level, skills hub = interop/`kronael/tools` tap |
| [5-components-toolbox.md](5-components-toolbox.md)                   | draft   | arizuko as a toolbox: which components are usable separately (measured by import coupling), how to present them — crackbox + obs ship now, resreg → `mcpfw`     |
| [6-component-extraction-method.md](6-component-extraction-method.md) | draft   | the honest method behind 6/5: import-purity ≠ component; the three questions (contract / does-it-hold / stranger-vs-cruft); go deep on one, crackbox first      |
| [7-orthogonal-components.md](7-orthogonal-components.md)             | draft   | the sibling-component PATTERN: build/test/ship/run without arizuko; zero arizuko-internal imports; consumed via CLI/HTTP/`pkg/`. Anchors 8–13. (was `12/A`)     |
| [8-crackbox-standalone.md](8-crackbox-standalone.md)                 | shipped | egred — forward proxy with per-source allowlists; arizuko's per-folder egress consumer (was `12/9`)                                                             |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)                 | shipped | crackbox `pkg/host/` KVM/qemu sandbox library; Docker→KVM backend (was `12/12`)                                                                                 |
| [10-crackbox-dns-filter.md](10-crackbox-dns-filter.md)               | draft   | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host` (was `12/15`)                                                                                     |
| [11-messaging-gateway.md](11-messaging-gateway.md)                   | draft   | generic message router over opaque ids; routd adds folder/grant on top (was `12/16`)                                                                            |
| [12-mcp-firewall.md](12-mcp-firewall.md)                             | draft   | transparent MCP proxy; deny-wins tool-call filter (was `12/17`)                                                                                                 |
| [13-sandd.md](13-sandd.md)                                           | draft   | sandbox-spawn daemon (was `12/c`)                                                                                                                               |
| [14-self-learning.md](14-self-learning.md)                           | draft   | pattern recognition → operator-gated proposals (skill/memory/persona) (was `12/7`)                                                                              |
| [15-skill-guard.md](15-skill-guard.md)                               | draft   | threat-pattern PreToolUse hook on agent-written skills (was `12/8`)                                                                                             |

`1–6` are the adoption/toolbox narrative; `7–15` are the component specs
consolidated from the former phase 12 (2026-07-14). `7` is the pattern that
anchors them; `8–10` the crackbox family (egress/sandbox/DNS); `11–13` the
gateway/mcp-firewall/sandd siblings; `14–15` the security features that ship
as components.

## Ties

`specs/5/A` (positioning) · `USELESS.md` (the honest gaps this closes)
· the Agent Research Hub · `6/14` self-learning (the loop turned outward).

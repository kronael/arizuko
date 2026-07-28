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

| Spec                                                             | Status    | Covers                                                                                                                                                                                                                                                                                                                           |
| ---------------------------------------------------------------- | --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [1-adoption-interop.md](1-adoption-interop.md)                   | draft     | interop-first strategy + the agentic reimplementation loop + the services a wrap provides + demand-class mode (absorbed `3`+`4`, 2026-07-16)                                                                                                                                                                                     |
| [2-target-matrix.md](2-target-matrix.md)                         | reference | competitive intel: which hub systems to incorporate (mechanisms) vs beat (products). Living Hub analysis, not a design spec                                                                                                                                                                                                      |
| [8-crackbox-standalone.md](8-crackbox-standalone.md)             | shipped   | crackbox — forward proxy with per-source allowlists; arizuko's per-folder egress consumer (was `12/9`)                                                                                                                                                                                                                           |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)             | shipped   | crackbox `pkg/host/` KVM/qemu sandbox library; Docker→KVM backend (was `12/12`)                                                                                                                                                                                                                                                  |
| [10-crackbox-dns-filter.md](10-crackbox-dns-filter.md)           | shipped   | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host` (was `12/15`)                                                                                                                                                                                                                                                      |
| [12-mcp-firewall.md](12-mcp-firewall.md)                         | draft     | transparent MCP proxy for THIRD-PARTY MCP servers; deny-wins tool-call filter (was `12/17`)                                                                                                                                                                                                                                      |
| [17-agentic-distribution.md](17-agentic-distribution.md)         | reference | **recorded debate (not a build)**: could arizuko be "the Debian of agents" (products-as-packages + a deployment axis for multi-container products)? A fable draft + a codex demolition — verdict: it's `5/20` in distro vocabulary; fold the real deltas there, retire the framing. Kept as the reasoned exploration + critique. |
| [16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md) | draft     | **the "ship in parts" catalogue**: two standalone axes + the component contract (domain vs mechanism) + the three-question method + package & daemon matrices + use-case clusters + the counterpart landscape & promote-vs-perfect (absorbed `5`+`6`+`7`, 2026-07-16)                                                            |
| [18-worlds-guests-oauth.md](18-worlds-guests-oauth.md)           | draft     | **the collapsed hierarchy**: World (users onboard) → Agent (a main group) → Session (subagent spawns, auto-onboard, prototyping) — replacing `5/5`'s arbitrary-depth org-chart; all managed as MCP tools. Folds in guests + delegated OAuth (a guest links their own accounts; the agent acts as them under rules)               |

`1`–`2` are the adoption/competitive narrative; `16` is the component
framing (the contract + the catalogue); `8`–`10` (crackbox family:
egress/sandbox/DNS) + `12` (MCP firewall) are the concrete component specs
from the former phase 12. Dropped 2026-07-16: `3`+`4` (folded into `1`),
`5`+`6`+`7` (folded into `16`), `11` (messaging-gateway — premature) and
`13` (sandd — `runed` owns spawn). Self-learning + skill-guard moved to
`5/22`·`5/23` (agent features, not components).

## Ties

`specs/5/A` (positioning) · `USELESS.md` (the honest gaps this closes)
· the Agent Research Hub · `5/22` self-learning (the loop turned outward).

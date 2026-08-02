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

| Spec                                                             | Status  | Covers                                                                                                                                                                                                               |
| ---------------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [1-adoption-interop.md](1-adoption-interop.md)                   | draft   | interop-first strategy + the agentic reimplementation loop + the services a wrap provides + demand-class mode (absorbed `3`+`4`, 2026-07-16)                                                                         |
| [8-crackbox-standalone.md](8-crackbox-standalone.md)             | shipped | crackbox — forward proxy with per-source allowlists; arizuko's per-folder egress consumer (was `12/9`)                                                                                                               |
| [9-crackbox-sandboxing.md](9-crackbox-sandboxing.md)             | shipped | crackbox `pkg/host/` KVM/qemu sandbox library — **shipped but unwired**: no importer outside `crackbox/`; `runed` still spawns Docker (was `12/12`)                                                                  |
| [10-crackbox-dns-filter.md](10-crackbox-dns-filter.md)           | shipped | DNS NXDOMAIN filter on UDP/53; reuses `Registry`+`match.Host` (was `12/15`)                                                                                                                                          |
| [12-mcp-firewall.md](12-mcp-firewall.md)                         | draft   | `mcpfw` — transparent MCP proxy for THIRD-PARTY MCP servers; deny-wins tool-call filter (was `12/17`)                                                                                                                |
| [16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md) | draft   | **the "ship in parts" catalogue**: two standalone axes + the component contract (domain vs mechanism) + the three-question method + package & daemon matrices + use-case clusters (absorbed `5`+`6`+`7`, 2026-07-16) |

`1` is the adoption narrative; `16` is the component framing — the contract
plus the catalogue. `8`–`10` (crackbox family: egress/sandbox/DNS) and `12`
(`mcpfw`) are the concrete component specs from the former phase 12.

Dropped 2026-07-16: `3`+`4` (folded into `1`), `5`+`6`+`7` (folded into
`16`), `11` (messaging-gateway — premature), `13` (sandd — `runed` owns
spawn). Self-learning + skill-guard moved to `5/22`·`5/23` (agent features,
not components); the Hermes integration moved to
[`../5/35-hermes-integration.md`](../5/35-hermes-integration.md) 2026-08-02
(it consumes the ingress + auth surfaces phase 5 owns).

Deleted 2026-08-02:

| was                          | why                                                                                                                                                                  |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `2-target-matrix.md`         | competitive intel duplicating the Agent Research Hub, which it named as the real source of truth. Dated vendor tables rot; re-read the Hub instead.                  |
| `17-agentic-distribution.md` | self-disclaimed "recorded debate, NOT a build order" whose own codex critique folded it into `5/20`. The honest-removal delta landed in `5/28` (shipped 2026-07-29). |
| `slides.md`                  | a 10-slide pitch deck — presentation material, no buildable content.                                                                                                 |

`16`'s counterpart landscape was compressed to its durable finding (the
composition is the moat; never claim a commodity part is novel); the dated
2026-07-15 vendor sweep table was cut for the same reason `2` was.

## Ties

`specs/5/A` (positioning) · `USELESS.md` (the honest gaps this closes)
· the Agent Research Hub · `5/22` self-learning (the loop turned outward).

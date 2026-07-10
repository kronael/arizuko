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

| Spec                                                     | Status | Covers                                                                  |
| -------------------------------------------------------- | ------ | ----------------------------------------------------------------------- |
| [1-adoption-interop.md](1-adoption-interop.md)           | draft  | interop-first strategy, campaign, the agentic reimplementation loop     |
| [2-target-matrix.md](2-target-matrix.md)                 | draft  | which hub systems to incorporate (mechanisms) vs beat (products)        |
| [3-graph-taxonomy-answer.md](3-graph-taxonomy-answer.md) | draft  | ride the graph/taxonomy hype the non-intrusive way (folder = substrate) |

## Ties

`specs/5/A` (positioning) · `USELESS.md` (the honest gaps this closes)
· the Agent Research Hub · `specs/12/7` self-learning (the loop turned
outward).

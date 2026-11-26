---
status: active
---

# specs/6 — split arizuko into parts

The end goal is that people run arizuko instead of their own agent
harness. The path (`ARCHITECTURE.md` § Decided, and why): **publish the environment, do not wrap the
harness.** Like Lambda, arizuko publishes contracts — the agent
environment and the component environment — and the value is what the
environment gives you for free: tenancy, egress, ACL, web, audit.
Conformance is measured, not declared: `anteval` per backend for the agent,
a probe against a live process for the component. Wrapping a foreign
harness stays an on-ramp.

The operator cockpit shipped: every daemon serves its own `/dash/<daemon>/`
from its own `/v1` (`dashd/README.md`, `chanlib/README.md`).

## Specs

| Spec | Status | Covers |
| -------------------------------------------------------------------- | --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [44-delivery-contract.md](44-delivery-contract.md) | reference | **one delivery door**: a turn reaches a chat through a delivery tool call or reaches nothing, so the prose result stops being a delivery path and `appendAndDeliver` + `silentRefusalRe` are deleted; states what replaces the 2026-06-08 no-reply guard, defines the nudge as the tagged annotation line the tree already carries in four producers, rejects regex situation-detection for state detectors plus exact argument checks at the one door, subtracts 18 lines / 1,918 B from `ant/CLAUDE.md` under `ant/README.md` residency rule, and gates the whole change on the unmeasured prose-fallback rate |
| [46-turn-progress.md](46-turn-progress.md) | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested, the page in Chrome. Measured: one temporary `<status>` message works; the Claude task tools and the Codex plan tool are live since v0.75.29 and no long Claude or Codex turn has run since. Remaining: the adoption sample on such turns, the Drive probe (owner). |
| [49-approval-ask.md](49-approval-ask.md) | shipped | Approval text and declared standing keys share one renderer. krons and sloth store no copy of a skip default. |
| [53-per-group-harness.md](53-per-group-harness.md) | shipped | One harness document per group over the instance defaults; no other setting selects backend, model or effort, and dashd writes the document to the store itself. The default, pi and codex cases of `anteval/backends` passed on one disposable instance; the claude case waits for a credential (BUGS `L951`). |

`7` owns the contracts; `1` owns the strategy. `24` (the per-spawn egress
proxy) is the concrete component spec.

## Ties

`concepts/primitives.html` of the operator docs (positioning) · `anteval/README.md` (the agent conformance basis) · `chanlib/README.md` (channel protocol) · `USELESS.md` (the honest gaps this
closes) · the Agent Research Hub · self-learning (`ARCHITECTURE.md` § Decided, and why; the loop turned
outward).

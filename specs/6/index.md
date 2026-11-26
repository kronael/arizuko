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
| [34-resident-prompt.md](34-resident-prompt.md) | shipped | Resident manual and pointer guards exist; the full catalog ran on a disposable instance (14/24). The three platform and case defects it found are fixed in the tree and not yet rerun on an image. |
| [44-delivery-contract.md](44-delivery-contract.md) | reference | **one delivery door**: a turn reaches a chat through a delivery tool call or reaches nothing, so the prose result stops being a delivery path and `appendAndDeliver` + `silentRefusalRe` are deleted; states what replaces the 2026-06-08 no-reply guard, defines the nudge as the tagged annotation line the tree already carries in four producers, rejects regex situation-detection for state detectors plus exact argument checks at the one door, subtracts 18 lines / 1,918 B from `ant/CLAUDE.md` under `6/34`'s rule, and gates the whole change on the unmeasured prose-fallback rate |
| [45-gdrive-comments-channel.md](45-gdrive-comments-channel.md) | shipped | Decision: keep the tested reply channel and Sheets read tool; resolve/reopen need L910 approval. Real Google files, nine probes and live reply/edit/delete acceptance remain owner blockers. |
| [46-turn-progress.md](46-turn-progress.md) | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested, the page in Chrome. Measured: one temporary `<status>` message works; the Claude task tools and the Codex plan tool are live since v0.75.29 and no long Claude or Codex turn has run since. Remaining: the adoption sample on such turns, the Drive probe (owner). |
| [49-approval-ask.md](49-approval-ask.md) | shipped | Approval text and declared standing keys share one renderer. krons and sloth store no copy of a skip default. |
| [47-voluntary-reply.md](47-voluntary-reply.md) | shipped | Decision: keep the tested delivery check and one nudge; remove obsolete thinking-tag instructions. Image rollout, live acceptance and the weekly rate remain unmeasured; no rate guarantee. |
| [50-anteval-ephemeral-instance.md](50-anteval-ephemeral-instance.md) | shipped | `anteval run` creates, drives and removes its instance. Measured on a disposable instance against a scratch store: every path left nothing, a product run passed 2/2, the smoke 6/9 and the catalog 14/24; BUGS `L950`, `L952` and `L953` hold the open case failures. |
| [53-per-group-harness.md](53-per-group-harness.md) | shipped | One harness document per group over the instance defaults; no other setting selects backend, model or effort, and dashd writes the document to the store itself. The default, pi and codex cases of `anteval/backends` passed on one disposable instance; the claude case waits for a credential (BUGS `L951`). |

`7` owns the contracts; `1` owns the strategy. `24` (the per-spawn egress
proxy) is the concrete component spec.

## GitHub feature workflow review

| Spec                                                                 | Status  | Covers                                                                                                                                           |
| -------------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| [48-github-feature-improvement.md](48-github-feature-improvement.md) | shipped | Decision: ordinary feature work uses existing groups, skills and GitHub records; no core coordinator, paid experiment or self-release mechanism. |

## Ties

`concepts/primitives.html` of the operator docs (positioning) · `anteval/README.md` (the agent conformance basis) · `chanlib/README.md` (channel protocol) · `USELESS.md` (the honest gaps this
closes) · the Agent Research Hub · self-learning (`ARCHITECTURE.md` § Decided, and why; the loop turned
outward).

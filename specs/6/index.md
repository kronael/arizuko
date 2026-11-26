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
| [34-resident-prompt.md](34-resident-prompt.md) | defected | Resident manual and pointer guards exist; the full catalog ran on a disposable instance (14/24). `unknown-addressee`, the one moved rule with a case, fails: BUGS `L952`. |
| [44-delivery-contract.md](44-delivery-contract.md) | reference | **one delivery door**: a turn reaches a chat through a delivery tool call or reaches nothing, so the prose result stops being a delivery path and `appendAndDeliver` + `silentRefusalRe` are deleted; states what replaces the 2026-06-08 no-reply guard, defines the nudge as the tagged annotation line the tree already carries in four producers, rejects regex situation-detection for state detectors plus exact argument checks at the one door, subtracts 18 lines / 1,918 B from `ant/CLAUDE.md` under `6/34`'s rule, and gates the whole change on the unmeasured prose-fallback rate |
| [45-gdrive-comments-channel.md](45-gdrive-comments-channel.md) | partial | `googd` — Google Docs/Sheets/Slides comment threads as topics of a bound folder: polled `comments.list` (no comment-level push outside Developer Preview), a service account shared per document, one verb (`replies.create`) and no turn progress until Drive `edit` is probed (`../6/46`), a `gdrive_files` binding on routd; where "a document is a group" breaks; the Sheets read connector tool is declared, resolve and reopen are not (L910) |
| [46-turn-progress.md](46-turn-progress.md) | partial | Shared reducer, native snapshots, bounded progress, one reminder and browser replay/deletion are tested, the page in Chrome. Measured: one temporary `<status>` message works; no checklist reaches a chat. Remaining: an image with the Claude task-tool opt-in and a new sample, the Codex plan tool (L936), the GitHub repository-chat notice (L938), the Drive probe. |
| [49-approval-ask.md](49-approval-ask.md) | shipped | Approval text and declared standing keys share one renderer. krons and sloth store no copy of a skip default. |
| [47-voluntary-reply.md](47-voluntary-reply.md) | partial | Addressed delivery, one shared nudge, hooks and fresh follow-up identity are tested. Sloth silence/reply/correction acceptance, disposable Codex smoke, the weekly nudge outcome measurement, the `<think>` convention in packaged skills and two docs naming deleted code remain. |
| [50-anteval-ephemeral-instance.md](50-anteval-ephemeral-instance.md) | shipped | `anteval run` creates, drives and removes its instance. Measured on a disposable instance against a scratch store: every path left nothing, a product run passed 2/2, the smoke 6/9 and the catalog 14/24; BUGS `L950`, `L952` and `L953` hold the open case failures. |
| [53-per-group-harness.md](53-per-group-harness.md) | shipped | One harness document per group over the instance defaults; no other setting selects backend, model or effort, and dashd writes the document to the store itself. The default, pi and codex cases of `anteval/backends` passed on one disposable instance; the suite holds no claude case (BUGS record `L951`). |
| [54-agent-machine.md](54-agent-machine.md) | draft | A customizable persistent machine and runtime, reached as a Unix user through SSH; private disk, installed software and explicit reset. |
| [55-identity-held-credentials.md](55-identity-held-credentials.md) | draft | A user's credential pays for that user's turn; delivery, enrollment and internal funding await owner decisions. |
| [56-identity-held-tools.md](56-identity-held-tools.md) | queued | Tools carried by an OAuth identity and whose account a call acts as; the agent's own login stays the default and acting as a human stays a grant. |
| [59-local-daemons.md](59-local-daemons.md) | draft | One package lifecycle for core and outside daemons; Compose, socket discovery, scoped auth and store enrollment. |

`7` owns the contracts; `1` owns the strategy. `24` (the per-spawn egress
proxy) is the concrete component spec.

## GitHub feature workflow review

| Spec                                                                 | Status | Covers                                                                                            |
| -------------------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------- |
| [48-github-feature-improvement.md](48-github-feature-improvement.md) | draft  | Placement proposals, decision owners, GitHub feature experiments, and package revision boundaries |

## Ties

`concepts/primitives.html` of the operator docs (positioning) · `5/9` (`anteval` — the agent conformance
basis) · `chanlib/README.md` (channel protocol) · `USELESS.md` (the honest gaps this
closes) · the Agent Research Hub · self-learning (`ARCHITECTURE.md` § Decided, and why; the loop turned
outward).

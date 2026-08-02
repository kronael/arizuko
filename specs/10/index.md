---
status: active
---

# specs/10 — self-healing

A deliberate import of the load-bearing mechanisms from
[Aeon](https://github.com/aaronjmars/aeon), a GitHub-Actions agent framework
with a "self-healing" reputation. Aeon runs on cron in GHA; arizuko is a
channel-native multi-tenant platform. The architecture does not port. The
mechanisms do.

Aeon's self-healing reduces to one loop — judge the run, evaluate the
history, fire a repair — and arizuko has none of it today. Source analysis:
`reference_aeon.md` (memory).

| Spec                                             | Status | Hook                                                                                                                                       |
| ------------------------------------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------------------------ |
| [1-self-healing-loop.md](1-self-healing-loop.md) | draft  | the whole loop: Haiku-as-judge → `skill_health`, a state-evaluator module in `timed` → `verb=state-event`, `/repair-*` playbooks as skills |

**Nothing here is built.** `skill_health` and `cooldowns` have zero hits in
the schema, and no `ant/skills/repair-*` exists.

## Design principle

Don't introduce new primitives where the messaging primitive carries the
seam. The evaluator is a message producer like `timed`; playbooks are
skills, not infrastructure; the judge is a sub-call at container exit. The
only new surface is two tables and one write tool.

## Skipped Aeon mechanisms

Regression-hunter / git-blame (useful for the arizuko _codebase_, not for
agents); cluster-first triage (defer until `skill_health` has signal to
chew on); pull-based Telegram scheduling (we have instant webhooks);
git-as-database (SQLite + per-folder files is more flexible — and phase 9
rejected git-as-truth outright).

## Merged and deleted 2026-08-02

| was                              | now                                                                                                                                                            |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `1-self-eval-haiku.md`           | merged into `1-self-healing-loop.md` — they were one loop split across three files, each inert alone                                                           |
| `2-state-evaluator.md`           | merged into `1-self-healing-loop.md`                                                                                                                           |
| `3-repair-playbooks.md`          | merged into `1-self-healing-loop.md`; the five per-playbook recipes were cut — a recipe is a skill body, not a spec                                            |
| `4-positioning-componentized.md` | deleted. Superseded by `5/A-primitives-framing.md` + `6/16-daemon-standalone-matrix.md`, and actively wrong — it listed `gated` as a live schema-owning daemon |
| `5-skill-catalog-audit.md`       | deleted. A methodology that was never run, headlining "arizuko's 56 skills" when `ant/skills/` carries 93                                                      |

The merge also corrected the loop for the split: `routd` owns both tables,
because `timed` owns no DB and reads over its service bearer. The originals
had `gated` owning the migrations.

`4` argued for dropping `13/6-workflows.md` in favour of "any automation is
a folder". That call was never actioned in its own file; phase 13 was
dissolved on 2026-08-02 and the workflowd idea went with it.

---
name: service-eval
description: Evaluate the live arizuko fleet and named tenant groups through the canonical operational checks. NOT for product adoption or adversarial review; use cto-eval, ceo-eval or red-eval.
when_to_use: "service eval, eval all, eval rhias, evaluate fleet health, understand current agent behavior"
user-invocable: true
---

# Live service evaluation

Read `../eval/SKILL.md`, `../eval/checks.md` and `../eval/gates.md`.
Those files own the checks and thresholds. Do not copy their criteria here.

Discover loaded instance units and registered groups. A named tenant such as
`rhias` is a group namespace; resolve its instance and adapters from live data.
Inspect all 23 checks. Allow 10 minutes for the read-only pass; record unfinished
checks as coverage gaps. Use unit journals and catalog-resolved read-only queries.
Never execute the mutation examples in the check files during evaluation.
Never post a chat message, restart a service, change a grant or remove history.

For each check, record its source, result and measured failure in the active
change's `.claude/plans/` record. On failure, identify the smallest repair and
record it in `BUGS.md`. User-authorized repairs follow the project bug workflow;
an evaluation does not authorize repairs by itself.

Separate addressed work from intentional observation, active tenants from dormant
groups, and present failures from historical records. Check actual memory paths,
including `.diary`, and respect explicit skill-disable controls. Do not change
files merely to make freshness or file-count checks pass.

Validate a positive and negative control for each automated assertion. Use
current healthy evidence and known incident samples or local fixtures. An
assertion without a negative control remains unvalidated. Name uncovered
failure classes from `BUGS.md`; do not claim the routine pass covers every defect.

Live-agent smoke tests and private task probes belong to authorized deployment
verification, outside this read-only pass. A fleet verdict remains conditional
until the required smoke gates and any task-specific behavior checks pass.

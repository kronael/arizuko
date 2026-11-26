---
status: shipped
---

# Keep GitHub feature work outside core orchestration

A feature workflow turns a request into an accepted code change through the
existing groups, GitHub messages, skills and review.

**Shipped decision:** do not build a feature coordinator, an instruction
experiment driver or self-release machinery in arizuko's core. Ordinary feature
work already uses the platform's message and execution contracts. No paid
experiment or production activation is authorized by this decision.

## Existing components carry the ordinary workflow

[gitd](../../gitd/README.md) maps signed GitHub events to messages and posts
replies. [routd](../../routd/README.md) selects the group and topic.
[runed](../../runed/README.md) executes an isolated turn.
[ant](../../ant/README.md) selects Claude, Codex or Pi with the same delivery
tools. The workflow belongs to the selected group's instructions and grants.

`ant/skills/arizuko-issue/SKILL.md` records group-local reports. It does not
install a fleet-wide queue or a maintenance coordinator. GitHub issues, linked
PRs, checks and reviews can hold feature status without another work table.
An issue and a PR have different topics, so a workflow follows their explicit
link. A root label never grants access to another group's reports.

## A coordinator would replace existing work records

A core coordinator would duplicate GitHub issue and PR state, then couple
routd dispatch to repository strategy. No measured request needs that extra
state machine. Package installation already has one lifecycle in
[EXTENDING.md](../../EXTENDING.md#installing-packages). Exact revision
activation and recovery would need changes to that lifecycle; a recorded
revision alone does not supply either guarantee.

A model-driven instruction experiment answers a separate question: whether an
instruction edit improves accepted features at equal cost. A passing PR is not
that evidence. Automatic deployment answers another question and requires
separate authority and an external recovery path.

## A bounded experiment costs more than ordinary repair

Estimated cost of an operator-invoked local comparison command: **3–5
engineering days**, plus model spend and owner review. The owner must set the
repository, issue sample, acceptance checks and monetary budget before any run.
No monetary estimate is possible without that workload and chosen model.

The command would replace manual matched execution comparisons, not routd or
runed. It must isolate candidates, preserve the baseline, count every attempted
run and prevent instruction edits from changing permissions or acceptance tests.
Release activation adds artifact selection, independent checks and failure
recovery. No implementation estimate is credible until that contract is chosen.

## Build only after a measured workflow failure

A concrete feature request that existing skills cannot coordinate may justify
an independently packaged tool. Evidence that controlled instruction edits
improve accepted features may justify a separately budgeted experiment.
Neither condition follows from wanting the agent to improve features.

There is no new schema, tool surface, process, cache or installer. Messages and
logs keep their existing retention; only `audit_log` expires after 90 days.
The decision adds no dashboard control because it adds no managed resource.
Source inspection at `880119f1b75548b3d3fdbab40d4eda526f3fd060` supports these
boundaries. No experiment result or self-release acceptance is claimed.

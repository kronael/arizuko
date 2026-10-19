---
status: draft
---

# GitHub feature improvement

A feature workflow turns user requests into reviewed code changes and tests
whether better instructions improve that work.

This draft proposes ownership choices for review. It recommends existing skills
and an operator-invoked experiment, with release recovery outside the deployment
being changed. Implementation and paid experiments require separate approval.

## Problem and existing boundaries

The outcome is an accepted feature, not an opened PR or a successful build.
Instruction changes need evidence that they improve this outcome beyond ordinary
code repair. Routing, execution, instruction experiments, and release activation
remain separate concerns.

Current behavior is checked at
`e0b913b56601978f07c2b65f4106187ff9b531b4`:

| Owner                                        | Existing responsibility                                                                                                                |
| -------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| [gitd](../../gitd/README.md)                 | Receives signed GitHub events and delivers conversation messages.                                                                      |
| [routd](../../routd/README.md)               | Routes messages to groups and topics; it does not select feature strategies.                                                           |
| [runed](../../runed/README.md)               | Runs isolated turns through the existing runtime contract.                                                                             |
| [ant/Pi](../../ant/src/pi.ts)                | Runs the configured executor with explicit skills and the platform policy extension. Pi's current profile has no platform tool bridge. |
| [Package CLI](../../cmd/arizuko/packages.go) | Resolves sources and records installed assets; [products](../../cmd/arizuko/products.go) apply group-scoped mixes.                     |

## Placement proposals

| Placement                                    | Reason to select it                                                                                                                | Cost                                                                                                     |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| A. Skills and an external experiment command | Recommended starting point. A maintenance group prepares specs and feature PRs; a manually invoked command compares Pi executions. | Reuses GitHub work records and existing execution. No new core job model or service.                     |
| B. Packaged coordinator                      | Select if repeated experiments demonstrate a need for durable multi-run coordination.                                              | Adds an independently usable component and its lifecycle; management uses existing resource conventions. |
| C. Core orchestration in routd               | Select only if several unrelated workflows require the same durable coordination.                                                  | Couples platform dispatch to workflow state. A GitHub-only case does not justify it.                     |

Harness² is a possible experiment driver for A or B, not another ownership
placement. Adopting it needs repository-task and Pi-executor adapters; compare
that cost with a small command before choosing. None of these placements puts
feature planning inside runed or requires a Pi fork. A Pi extension earns its
place only through a measured need that skills and recorded execution cannot meet.

## Proposed flow and decision owners

```text
Issue / submitted report ► maintenance group ► spec ► feature PR
                                  │                     │
                                  ▼                     ▼
                           runed ► ant ► Pi       checks / review

Execution evidence ► instruction experiment ► workflow revision PR

Approved release ► external activation / recovery ► acceptance
```

Users can file GitHub issues or submit reports through the existing
[issues skill](../../ant/skills/issues/SKILL.md). That skill writes a group-local
file, not a fleet-wide queue. The designated maintenance agent consolidates
reports it is authorized to read, preserving source IDs and links. Calling an
agent root does not grant access to other groups.

The operator's route table chooses the group. The group's approved skills and
configured backend choose the workflow and executor. The workflow interprets
the request and clarifies acceptance criteria; that interpretation grants no
permission. Checks drive bounded code repair. Only an explicit experiment may
change instructions; the experiment command enforces its editable scope and
budget. Independent checks and owner review decide adoption.

GitHub issues, linked PRs, checks, and review hold work status. An issue and its
PR have different gitd topics, so the workflow follows their explicit link and
the current PR commit rather than relying on shared conversation history.
CI events are observation context by default; the first experiment uses an
explicit invocation, not automatic wake-on-check behavior.

## Lessons adopted from Google Research

The [Harness² paper](https://arxiv.org/html/2610.14224v1) supports matched
execution comparisons and selective reuse. Behavioral differences guide edits;
they do not establish correctness or transfer. The experiment adopts these rules:

- **Propose, probe, compose:** identify a concrete execution failure, change one
  instruction component, compare its execution with the baseline, and retain only
  supported edits. If composition changes the candidate, execute it again.
- **Keep the baseline:** unchanged instructions and no durable lesson are valid
  outcomes. Prefer deletion and simpler models, views, and state handling before
  additions; judge required behavior, not line count alone.
- **Attribute carefully:** record each edit's diff, run evidence, and observed
  effect: helped, hurt, no observed effect, or confounded. One rollout or several
  simultaneous edits cannot establish which instruction caused a difference.
- **Learn selectively:** retain at most one reusable lesson per experiment,
  stating its trigger, operation, evidence, and confidence, or retain none. Store
  the executable revision with the evidence through existing Git work records;
  a selected output alone does not prove its edits helped.
- **Start clean:** each new task starts from the common approved workflow.
  Retrieve relevant earlier lessons to guide an explicit proposal; do not inherit
  the last task's instructions or facts automatically.

These rules adapt the research
[prompts](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/prompts.py)
and [experience library](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/library.py).
The released final selector excludes original baseline executions. This proposal
keeps the baseline eligible and uses independent acceptance checks.

## Experiment contract

Replay historical feature issues from fixed repository commits, hiding accepted
solutions and later discussions. Compare fixed instructions, ordinary
check-driven code repair, and evidence-driven instruction changes at equal total
budgets. Count executor, editor, evaluator, and failed-attempt costs. Repeat runs
to measure variation and reserve separate issues to test transfer.

Hold the model, environment, input state, and acceptance checks fixed. Candidate
workspaces and sessions are isolated. Measure feature acceptance, regressions,
review effort, elapsed time, total spend, and instruction size. Reports of missing
grants, broken infrastructure, or exhausted quota are not instruction failures.

Independent checks and reserved issues are this proposal's controls, not guarantees
established by the research. The proposer cannot modify acceptance checks,
evaluator, execution policy, permissions, credentials, or its budget. Raw traces
stay private; only reviewed, sanitized lessons enter shared workflow PRs. Feature
diffs and instruction diffs stay separate. No paid live anteval becomes a scheduled
or CI dependency.

Candidates receive no production credentials or publication authority. Issue text,
repository files, and execution evidence are data, not instructions to the driver.

## Package revisions and self-release

A feature commit, a workflow/product commit, and a runtime image are separate
revisions. Candidate workflow commits can be replayed in private workspaces
without changing the installed package.

The existing [package contract](../5/28-packages.md) records source, revision,
owned identities, and hashes. The CLI's `fetchSource` shallow-clones HEAD;
`reapplyPackage` refreshes file assets without replaying owned rows or skills.
`applyPackageSkills` installs to the instance-wide layer. A recorded revision
therefore does not establish exact revision activation or full rollback.

If live workflow revision selection becomes necessary, extend that same lifecycle
and [group product mix](../../cmd/arizuko/products.go), with conflict refusal and
verified restoration. Do not add a second installer, manifest, or revision
registry. Package rollback does not undo merged PRs, messages, external writes,
or irreversible migrations.

Issue collection and patch preparation can run inside Arizuko without authority
to deploy Arizuko. The recommendation is an external operator-invoked release
command that survives a broken deployment. A narrow MCP request/status surface
is optional, not a prerequisite; Pi's current missing platform-tool bridge must
be resolved separately if Pi itself needs that surface.

Any self-release has one pending patch at a time across the affected deployment.
Other requests can queue. The pending patch ends only after independent acceptance,
verified restoration, or verified cancellation before activation. Restart or
uncertain execution cannot silently admit another patch.

Before activation, preserve the last accepted immutable artifacts, configuration,
and required recoverable data. Checks outside the candidate's control must cause
restoration on failure. Failed restoration blocks further changes and requires
operator recovery that works without Arizuko. Changes with irreversible effects
need an approved recovery procedure before activation. Reverting Git alone is
not that procedure. No new state machine or lease protocol is selected here.

## Decisions for review

Select A, B, or C; then choose a small local command or Harness² as the experiment
driver. The recommendation is A with a manually invoked command and Pi as the
initial executor. Select the repository, issue sample, acceptance authority,
budget, and publication boundary before running models. Self-release activation
and live package rollback remain separate approval decisions.

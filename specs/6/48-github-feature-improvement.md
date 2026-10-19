---
status: draft
---

# GitHub feature improvement

A feature workflow turns a GitHub request into a reviewed code change and tests
whether changes to the agent's instructions improve delivery.

This draft compares placements for owner review. The recommendation is a
group-scoped workflow executed by Pi, with an external experiment driver before
any platform extension. Approval of this document does not approve implementation,
paid experiments, automatic merging, or live package changes.

## Problem

Successful compilation does not establish that an agent implemented the requested
feature. A useful experiment measures the complete issue-to-PR result and the
human effort needed to accept it. Repeatedly adding instructions also needs a
test: a learned rule can consume context without improving later work.

The first decision concerns who owns the work, not which daemon gains a loop.
Message routing, execution, feature repair, instruction experiments, and package
activation remain separate concerns.

## Existing boundaries

These observations refer to the PR base commit
`e0b913b56601978f07c2b65f4106187ff9b531b4`.

| Owner        | Existing responsibility and source                                                                                                                                            |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| gitd         | Signed GitHub events, issue/PR addresses, and conversation delivery: [gitd/README.md](../../gitd/README.md).                                                                  |
| routd        | Routes messages to a folder and topic; package records reside here: [routd/packages_store.go:1](../../routd/packages_store.go).                                               |
| runed        | Isolated execution through the existing runtime contract: [runed/README.md](../../runed/README.md).                                                                           |
| ant          | Selects the configured executor and renders its context: [ant/src/backend.ts:1](../../ant/src/backend.ts), [ant/src/index.ts:1](../../ant/src/index.ts).                      |
| Pi           | Executes a turn with explicit skills and the platform policy extension: [ant/src/pi.ts:43](../../ant/src/pi.ts), [ant/src/pi-extension.ts:1](../../ant/src/pi-extension.ts).  |
| Operator CLI | Installs packages and applies group product mixes: [cmd/arizuko/packages.go:136](../../cmd/arizuko/packages.go), [cmd/arizuko/products.go:89](../../cmd/arizuko/products.go). |
| anteval      | Tests live platform capabilities through public outcomes: [anteval/README.md](../../anteval/README.md). Repository feature acceptance is a separate experiment.               |

## Placement proposals

| Proposal                                          | Placement and reasoning                                                                                                                                           | Cost and selection condition                                                                                                                              |
| ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A. Workflow skill plus external experiment driver | A group product supplies the GitHub feature skill and deterministic helpers. Pi executes repository work; a manually invoked driver compares isolated executions. | Recommended first. Exercises existing boundaries without a core loop, package schema, or runtime port change.                                             |
| B. Packaged coordinator                           | An independently usable component owns issue jobs, experiment budgets, candidate runs, and evidence. Arizuko supplies ingress, permissions, and execution.        | Select only if resumable multi-run work exceeds the skill's measured capability. Adds a service lifecycle and, if needed, owned management resources.     |
| C. Core orchestration                             | routd owns an experiment job and dispatches candidate turns through runed.                                                                                        | Highest coupling. Select only if multiple unrelated workflows require the same durable orchestration; a GitHub-only case does not justify this placement. |
| D. Harness² as an external driver                 | Add a repository-task adapter and Pi executor adapter to the research framework, keeping Arizuko outside its search logic.                                        | Useful comparison if adapter work is smaller than a local driver. Its benchmark lifecycle and editable surface need explicit restriction.                 |

No proposal puts feature planning, candidate selection, or lessons inside
runed. A Pi extension belongs only to a measured event-level requirement that
skills and existing execution records cannot satisfy. The first experiment
uses Pi's existing subprocess integration, not a Pi fork or an SDK migration.

The recommendation is A for the experiment. B is the escalation path if the
experiment proves useful and durable coordination earns its operational cost.
C and D remain reviewable alternatives, not concurrent implementations.

## Proposed main flow and decision owners

### Intake, preparation, and release are separate authorities

Users can file GitHub issues or record requests through the existing
[issues skill](../../ant/skills/issues/SKILL.md). The skill appends a local
group file; it is not a fleet-wide collector. A designated maintenance group
can consolidate authorized reports into GitHub issues and draft specs, keeping
source IDs for deduplication. Access to another group's report requires an
explicit grant or a deliberate submission from that group, not root-by-name.

The maintenance agent may prepare specs and feature PRs without production
deployment authority. The same GitHub workflow can serve Arizuko or an unrelated
repository. Running its planning inside Arizuko does not require Arizuko to
control its own release.

For changes to Arizuko itself, the recommendation is an external release runner
that survives a broken Arizuko deployment. A narrow MCP surface can submit a
reviewed release request and read its status. The runner verifies the approved
repository, base commit, candidate artifact, checks, and activation permission;
it does not accept arbitrary shell commands or agent-written health checks.
An MCP request is a control boundary, not a reason to place builds in routd.

The external runner can start as an operator-invoked command. A persistent
service and new resources require a measured need. If such a service is added,
its management surface follows resreg rather than bespoke privileged IPC.

### One pending patch per target

For self-change, one patch owns the target's release slot from accepted proposal
through final acceptance or verified restoration. Requests can queue, but a
second patch cannot build a dependent release or activate against an unaccepted
candidate. The slot identifies a repository and deployment target; a target with
shared artifacts or migrations shares the slot with those dependants. An
Arizuko self-release starts with one world-wide slot across its affected fleet.

The proposed states are proposed, preparing, ready, activating, verifying,
accepted, restoring, restored, and blocked. Acquisition is atomic and survives
runner restart. Repeated submissions identify the same patch. A stale base is
refused; a crashed worker or expired lease cannot release the slot while
activation or restoration remains uncertain. Only accepted, verified restored,
or verified cancellation before activation releases it.

Before activation, the runner preserves the last accepted immutable artifacts,
configuration, and any required recoverable data checkpoint. Independent health
and functional checks have a deadline; failure restores the accepted release
and verifies that restoration. If restoration fails, the runner stays blocked
and alerts the operator. A manual recovery path works without Arizuko or its
MCP endpoint. Old artifacts remain available until the patch is resolved.

Automatic restoration is admitted only for changes whose data contract permits
it. An irreversible migration or external action requires a separate approved
recovery procedure, not a promise that reverting Git undoes it. The runner holds
its authority and evidence outside the deployment being replaced. Production
activation never follows solely from the proposing agent's success report.

This one-patch restriction governs self-release, not ordinary issue intake.
Feature preparation for unrelated repositories need not share that global slot.

```text
GitHub issue ► gitd ► routd route ► group / issue topic
                                         │
                                         ▼
                                  feature workflow
                                         │
                                  runed ► ant ► Pi
                                         │
                                         ▼
                             repository checks / review
                                         │
                                         ▼
                                one draft feature PR

Failed execution ► instruction experiment ► isolated comparisons
                                              │
                                              ▼
                                 separate workflow revision PR
                                              │
                                              ▼
                                 owner approval / activation
```

| Decision                           | Owner and rule                                                                                                                                                             |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Which group receives an event?     | The operator's ordinary route table. gitd supplies the address and topic; it does not rank agents.                                                                         |
| Which workflow and executor run?   | The group's approved product/skill and configured backend. No model-selected backend switch is introduced.                                                                 |
| Is this a feature task?            | The workflow interprets an addressed request against its scope and asks about missing acceptance criteria. This interpretation grants no new permission.                   |
| Retry code or change instructions? | Ordinary checks first drive bounded feature repair. An explicit experiment invocation may propose an instruction change after identifying a repeatable behavioral failure. |
| Which candidate runs?              | The experiment driver admits only approved editable files and enforces the total budget; Pi proposes content within that boundary.                                         |
| Which result wins?                 | Independent checks establish behavior; review resolves incomplete requirements and compares diff quality. The unchanged baseline stays eligible.                           |
| What becomes durable?              | The owner reviews a separate workflow revision PR with execution evidence and results on reserved tasks. Merge and activation remain distinct decisions.                   |

An issue and its PR need explicit correlation because gitd assigns them different
topics. The feature PR links its issue; the workflow resumes from that link and
the checked-out PR head when review arrives. Topic history alone is not a job
record. Proposal A uses GitHub's existing issue, PR, and check state as the work
record, without introducing a second status database.

CI events are observation context under gitd's default routes. The initial flow
uses an explicit request to repair or review; autonomous wake-on-check behavior
needs an explicit route and budget policy. Bot-event filtering must stay intact.

## Proposed experiment

The experimental unit is one issue plus a fixed repository commit, acceptance
criteria, environment, model, and budget. Candidate workspaces start from the
same state; session history and writable data do not cross candidates.
Historical replay hides the accepted patch and later discussions from agents.
An independent evaluator may use those records without exposing them as inputs.

Compare three treatments: fixed workflow, fixed workflow with ordinary
check-driven repair, and a workflow with evidence-driven instruction changes.
Equal total budgets include executor calls, editors, evaluators, and failed
attempts. Repeated runs distinguish a lucky sample from a useful change.

The outcome is an acceptable feature diff, not persuasive prose or an opened
PR. Independent acceptance checks, existing regressions, review effort, elapsed
time, total spend, and changed instruction size form the evidence. Reserved
issues test transfer; their results do not feed candidate generation.

Instruction edits and feature edits stay in different diffs. The proposer may
simplify or delete skills and tool guidance; it may not change the evaluator,
acceptance checks, permissions, credentials, execution policy, or spend limit.
An unchanged workflow and no durable lesson are valid results.

## Proposed package revision and rollback contract

Three revisions have different meanings: the target repository's feature
commit, the workflow/product's source commit, and the platform's runtime image.
Reverting one does not revert the others.

The installed-package record stores source, resolved revision, owned identities,
and hashes. A group product shares that record keyed by folder and name through
`applyProductMix`; it is not another package manager. Existing contracts:
[5/28-packages](../5/28-packages.md) and
[7/25-publish-plan](../7/25-publish-plan.md).

The current CLI does not establish arbitrary revision activation or complete
rollback. `fetchSource` shallow-clones a source's HEAD; the product declaration
carries a source string. `reapplyPackage` updates file assets and the recorded
revision, preserving recorded row and skill identities without reapplying those
assets. `applyPackageSkills` places skills in the instance-wide layer. Therefore
a live instance-wide package upgrade is not candidate isolation, and a stored
commit hash is not proof that every effect can be undone.

For A, candidate revisions are Git commits materialized in private workspaces.
Only a selected revision becomes a workflow PR. This gives repeatable inputs
without claiming a live package rollback feature.

If B or live activation is selected, extend the existing source resolver and
product/package lifecycle with exact revision selection and a verified restore
contract. Do not add a second manifest, installer, skill index, or revision
registry. Activation applies only to new work; existing jobs retain their
recorded workflow revision. Restore must cover the effective files and owned
configuration, refuse local conflicts, and expose partial failure honestly.

A failed package apply's pre-image compensation differs from operator rollback
to an older revision. Neither undoes sent messages, merged feature PRs, external
API writes, or irreversible data migrations. Mixed infrastructure, grants, and
workflow edits cannot share a promise of automatic reversal. The experimental
workflow product excludes those infrastructure and grant changes.

## Permissions and evidence

The initial workflow performs work under existing group grants. Experiments
receive repository read access and disposable writable copies; publication uses
a separate authorized boundary. Issue text, repository files, logs, and model
outputs are untrusted input. Candidate code receives no deployment credentials
or privilege to change its evaluator or publication policy.

Raw private traces stay scoped to the group. Workflow revision PRs carry only
reviewed, sanitized evidence; source facts and secrets do not become shared
skills. No live anteval invocation becomes a scheduled or CI dependency.

If B or C adds operator-managed experiment records, those are cold-tier
resources owned by that component, registered through resreg with REST and MCP,
folder-bound authorization, audit, and an operator surface. Runtime execution
and delivery remain hot actions. A does not introduce these records or widen
the runtime port.

## Research and limits

[Harness²](https://github.com/google-research/harness2/tree/1cfe254e8d25b15f37048c3730134686abe60335)
provides propose/probe/compose and evidence-bound reflection. Its
[paper](https://arxiv.org/html/2610.14224v1) distinguishes behavior comparison
from correctness and identifies extra inference cost. Its supplied executors
are OpenCode and Codex; adoption for this flow needs Pi and repository adapters.
Its final selection pool excludes original baseline executions, so this proposal
retains the baseline explicitly rather than treating selection as improvement.

[Pi](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/README.md)
supports external automation and extensions. These support a driver without
requiring a core fork. No experiment here establishes that instruction search
beats ordinary repair at equal cost; that is the decision the experiment tests.

## Review decisions

The owner selects A, B, C, or D before implementation. The recommendation is A,
with a manually invoked comparison driver and Pi as the sole initial executor.
The owner also selects the repository, issue sample, acceptance authority, total
spend cap, and publication boundary before a model-backed experiment starts.
Exact live package activation and rollback remain a separately approved contract
if the selected approach requires them.

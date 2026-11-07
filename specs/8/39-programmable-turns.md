---
status: draft
---

# specs/8/39 — programmable turns: workflows of turns that arizuko runs

## What it is

A workflow is a small program of turns. arizuko starts each turn, carries each
turn's result to the turns that need it, and retries or stops a failed step.
A package declares the program as data, beside its scheduled tasks.

## The problem

Today a task is one prompt with an optional cron (`core/types.go:192`). A flow
of more than one turn exists only as prose inside a prompt: the agent must call
`schedule_task` or `delegate_group` itself (`ipc/ipc.go:2064`, depth capped at 1).
So the flow is invisible to the operator, a failed step cannot be retried alone,
and one turn that forgets a call ends the flow without an error.

A turn's recorded outcome is a status and a failure text only
(`store/turn_results.go:71`). The output of one turn has no place where a
second turn can read it.

Real cases that want this:

- scavenger (`~/sandbox/scavenger`): find, then value each find, then shortlist,
  then a human approves, then sell. Each step needs a different model and folder.
- sloth trading: a desk read, then an xhigh review of the saved report, then the
  final edit. Today one turn runs all three and the chat waits for the slowest.
- the memory rollups: day → week → month digests already run as separate cron
  tasks that must fire in order (`store/tasks.go:416`).

## Where it fits

```
┌──────────────────────┐ workflows (new) ┌──────────────────────┐
│ package arizuko.yml  ├────────────────►│ routd workflow runner│
│ scheduled_tasks      │                 │ (new)                │
└──────────┬───────────┘                 └───┬──────────────▲───┘
           │ trigger (changed)               │ enqueue step │ step result
           ▼                                 ▼              │ (new)
┌──────────────────────┐  due task  ┌──────────────────┐    │
│ timed fire loop      ├───────────►│ routd dispatch   │    │
└──────────────────────┘            └────────┬─────────┘    │
                                             │ turn         │
                                             ▼              │
                                    ┌──────────────────┐    │
                                    │ runed guest turn ├────┘
                                    │ submit_turn      │ output (changed)
                                    └──────────────────┘
```

arizuko stays the broker and the runner: it owns the queue, the per-folder
serialisation, the grants and the guest. The program only says which turn runs
next and with what input. The agent inside a turn does not orchestrate.

## Module map

```yaml
- path: resreg/resources/workflows.go (new)
  job: the workflows resource — a package or operator declares a program
  names:
    - 'Workflow(id, owner, trigger, steps[])'
    - 'Step(id, folder, prompt, needs[], model, effort, approve)'
  never: runs a step or reads turn output
- path: routd/workflow.go (new)
  job: the runner — on a trigger or a finished step, enqueues every step whose needs are done
  names:
    - 'advance(run_id) error'
    - 'WorkflowRun(id, workflow, step_states{}, started_at)'
  never: starts a guest itself; it enqueues through the existing dispatch path
- path: store/turn_results.go (changed)
  job: records the turn outcome plus a bounded output the next step reads
  names:
    - 'RecordTurnResult(folder, turnID, sessionID, status, failure, output)'
  never: holds more than the size limit; a large output is a file the step names
- path: ipc submit_turn (changed)
  job: carries the step output from the guest
  never: adds a second delivery path beside submit_turn
```

## The program is data

A step is a turn: a folder, a prompt, and the steps it needs. `needs` makes the
steps a graph, so fan-out (value each find in parallel) and fan-in (one
shortlist) need no extra word. A step's prompt receives its inputs as an
`<input step="...">` block, the same envelope shape a turn reads today.

```yaml
workflows:
  - id: scavenger-daily
    owner: scavenger
    trigger: { cron: '0 6 * * *' }
    steps:
      - {
          id: find,
          folder: scavenger,
          prompt: 'Pull CEVD and nabidkamajetku; list new lots.',
        }
      - {
          id: value,
          folder: scavenger/value,
          prompt: 'Price each lot against comparables.',
          needs: [find],
          effort: xhigh,
        }
      - {
          id: short,
          folder: scavenger,
          prompt: 'Pick the top 10.',
          needs: [value],
        }
      - { id: ok, approve: kronael, needs: [short] }
```

Hierarchy comes from two places that already exist: a step's folder can be a
child folder, which runs under that child's grants, and a step can name another
workflow as its program. A cron task stays what it is today: a workflow with one
step and a cron trigger. That makes the task the trigger, not a second scheduler.

`approve` is a step with no turn: the runner posts the input to the named person
and waits for an answer. It is the human gate before any sale or payment.

## Open questions

1. **Data or code?** A YAML graph is reviewable and safe; a script (like the
   harness Workflow tool's `agent()`/`parallel()`/`pipeline()`) is more
   powerful. Recommendation: data. Every current case is a fixed graph, and a
   script inside the broker is a second runtime to secure.
2. **Where does a step's output live?** A bounded text field in `turn_results`,
   or a file in the step folder that the next step reads. Recommendation: the
   field up to a small limit, a file path above it.
3. **Retry and failure.** Retry a failed step alone N times, then stop the run
   and post the failure to the owner chat. Is a partial run resumable by the
   operator? Recommendation: yes, from the failed step.
4. **Grants.** A step runs with the grants of its folder, and the run never
   gains more than the workflow owner holds over that subtree
   (`../5/33-paths-roles.md` §"Two axes"). Confirm that a workflow cannot reach
   outside its owner's subtree.
5. **Is it worth it?** The alternative is one turn that runs subagents. That
   already works, but it holds one guest for the whole flow, cannot use a
   different folder per step, and loses everything on one failure. The
   scavenger and trading cases need the per-step folder and the human gate.
6. **`next_run`.** Computing "due" on read from the cron (the open choice on
   `store.DueTasks`) is a precondition: a trigger must not depend on every
   writer storing a derived time.

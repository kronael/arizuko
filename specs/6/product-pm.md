---
status: planned
brand: sloth
---

# Product: PM agent (Sloth)

_Moves slowly. Forgets nothing. Has been watching your task board since before you opened it._

Project management agent embedded in a team's chat. Tracks tasks,
surfaces blockers, maintains the source of truth for what's in
progress, what's done, and what's at risk.
Template at `ant/examples/pm/`.

## Value prop

Replaces the "does anyone know the status of X?" question with an
agent that always knows. Maintains a living task board in facts/,
writes weekly status summaries, and flags what's slipping before
it's a problem.

## What it does

- **Task tracking**: maintains a task list in facts/tasks.md;
  updates on mention ("mark X done", "add Y with deadline Z")
- **Status summaries**: weekly digest of done / in-progress /
  blocked / at-risk items; delivered to the team channel on schedule
- **Decision log**: records decisions with context in facts/decisions/
  so new team members can catch up without reading all history
- **PRD drafting**: given a feature brief, produces a structured
  one-pager (problem / goals / non-goals / open questions / plan)
- **Blocker triage**: when a task is marked blocked, asks for details
  and pings the relevant owner in the channel

## Skills

| Skill           | Required | Notes                                            |
| --------------- | -------- | ------------------------------------------------ |
| diary           | yes      |                                                  |
| facts           | yes      | task list, decisions, milestones, team context   |
| recall-memories | yes      |                                                  |
| users           | yes      | knows who's who, their roles and current load    |
| web             | optional | for external reference (specs, docs, benchmarks) |
| oracle          | optional | for longer document synthesis (PRDs, retros)     |

No bash, no commit — PM agent reads and writes structured documents,
does not execute code or touch infra.

## Template folder

```
ant/examples/pm/
  SOUL.md       — precise, concise; keeps humans accountable without
                  being annoying; asks clarifying questions before
                  creating tasks; never invents scope
  CLAUDE.md     — task format: [status] owner: description (due: date);
                  update facts/tasks.md after every task mutation;
                  weekly digest on Fridays via scheduled task;
                  decisions go in facts/decisions/YYYYMMDD-slug.md
  skills/       — diary, facts, recall-memories, users
  facts/
    tasks.md    — seed: empty task board with format instructions
    team.md     — seed: who's on the team, roles, contact
  tasks.toml    — weekly Friday digest cron
```

## Channels

- Telegram or Discord — team channel (primary)
- slink — async access for distributed teams
- Email (emaid, optional) — weekly digest delivery to non-chat members

## Depends on

- timed — for weekly status digest
- users skill — team identity is central; without it task ownership
  is by name string only

## Branding note

Deployed as a PM agent on the sloth instance. SOUL.md and branding
are instance-configured. Template is domain-neutral: the team context
and project domain come entirely from the operator-seeded facts/.

## Open

- Task board format: markdown table vs structured TOML vs lightweight DB
- Milestone tracking: dates in facts/ vs integration with external tracker
- Velocity/burndown: derived from diary + task state changes over time

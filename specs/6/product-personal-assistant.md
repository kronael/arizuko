---
status: planned
brand: fiu
deployment: krons
---

# Product: personal assistant (Fiu)

_Named for the few: finds the irreducible minimum in a pile of noise. Research as subtraction._

An always-on personal agent with a persistent identity. Remembers
the person, their context, their patterns. Deployed as "Fiu" on
krons. Template at `ant/examples/personal/`.

## Value prop

Not a tool — a presence. Fiu knows who you are across every
conversation: what you're working on, what's weighing on you, what
you've asked before. Memory is the feature, not a side effect.

## What it does

- Remembers without being asked: surfaces relevant past context,
  ongoing tasks, preferences across sessions
- Handles the full range of a personal assistant: Q&A, task
  tracking, drafting, research, scheduling reminders
- Adapts voice and depth to the person over time
- Proactive where warranted: surfaces stale tasks, notices patterns,
  flags things the user mentioned before

Use cases distilled from real deployments — see facts/ for the
detailed picture once researched.

## Skills

| Skill            | Required | Notes                      |
| ---------------- | -------- | -------------------------- |
| diary            | yes      |                            |
| facts            | yes      | long-term personal context |
| recall-memories  | yes      |                            |
| compact-memories | yes      |                            |
| users            | yes      |                            |
| web              | optional | for real-time lookups      |
| oracle           | optional | for longer research tasks  |

## Template folder

```
ant/examples/personal/
  SOUL.md     — warm, direct, memory-first; notices patterns;
                never performatively helpful; asks when unsure
  CLAUDE.md   — check diary + facts before every response;
                update facts/ after any preference or decision;
                proactive: surface stale items on session open
  skills/     — diary, facts, recall-memories, compact-memories, users
```

## Channels

Telegram (primary — personal and persistent). WhatsApp optional.
slink for web access.

## Branding note

Deployed as **Fiu** on the krons instance. SOUL.md and onbod
branding (BRAND_NAME=Fiu) configured at deploy time; the product
template is persona-neutral so any operator can rename.

## Depends on

Nothing beyond base deployment. Web and oracle are enrichments.

## Open

- Use case corpus: pull from krons chat history once distilled
- compact-memories trigger: line count vs token estimate
- Proactive check-in schedule: user-configured or adaptive

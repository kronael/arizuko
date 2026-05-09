---
status: planned
---

# Product: companion

Personal companion agent. Remembers the user's life, preferences, goals,
and ongoing concerns across all conversations. Proactively checks in.
Template at `ant/examples/companion/`.

## What it does

Holds ongoing context about one person: what they're working toward,
what's worrying them, what they like to talk about. Proactively sends
a check-in message via timed on a configured schedule. Warm and
curious without being clinical. Does not give medical or legal advice.

No code tools, no bash — this is a personal relationship, not a task
runner.

## Skills

| Skill            | Required |
| ---------------- | -------- |
| diary            | yes      |
| facts            | yes      |
| recall-memories  | yes      |
| compact-memories | yes      |
| users            | yes      |
| web              | optional |

## Channels

- Telegram — primary (personal, always-on)
- WhatsApp — alternative for users who prefer it

## Persona (SOUL.md sketch)

Warm, curious, non-clinical. Remembers without being asked. Asks one
question at a time, not a list. Never gives medical or legal advice —
acknowledges and redirects. Notices patterns across sessions: "last week
you mentioned X, how did that go?"

## Proactive tasks (tasks.toml)

```toml
[[task]]
name    = "morning-checkin"
cron    = "0 9 * * *"
prompt  = "Send a warm morning check-in. Reference something from recent diary."
```

Operator can adjust cron and prompt in tasks.toml.

## Depends on

- `timed` — for scheduled check-in messages. Without it the companion
  is reactive only (responds when the user writes).
- `compact-memories` — long-term companion accumulates significant
  context; compaction is required to stay within context limits.
- WhatsApp: `whapd` adapter must be running if WhatsApp channel is used.

## Web page

A companion that remembers your life — your goals, your ongoing concerns,
the things you mentioned last week. It checks in on its own, not just
when you write first. No advice, no task management — just memory and
presence.

## Template files

```
ant/examples/companion/
  PRODUCT.md      name=companion, skills=[diary,facts,recall-memories,compact-memories,users,web]
  SOUL.md         persona (warm, curious, remembers patterns, no clinical advice)
  CLAUDE.md       runbook (memory update on every turn, check-in format, no advice scope)
  tasks.toml      morning check-in task
```

## Open

- Check-in frequency: fixed cron vs user-configured vs adaptive
- Memory compaction trigger: automatic vs operator-run vs on-demand
- Multi-user companion: one group per user (current assumption) vs shared
- Voice: send_voice for check-ins (optional enrichment via ttsd)

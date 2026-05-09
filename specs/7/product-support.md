---
status: planned
brand: atlas
---

# Product: support (Atlas)

_He held the sky so it would not fall. Not because he was asked — because someone had to._

Customer support agent embedded on a product site via the slink widget.
Answers questions from a knowledge base in facts/; escalates to a human
when stuck. Template at `ant/examples/support/`.

## What it does

Responds to inbound questions via the slink web widget. Looks up answers
in facts/ before web-searching. Registers new users via onbod so their
conversation history is retained across sessions. Escalates to a
configured Telegram group when it cannot answer confidently. Logs every
unresolved question to diary so the operator can fill gaps in facts/.

## Skills

| Skill           | Required |
| --------------- | -------- |
| diary           | yes      |
| facts           | yes      |
| recall-memories | yes      |
| users           | yes      |
| web             | optional |

No `bash`, no `commit`, no `oracle` — support agent has minimal
capability footprint.

## Channels

- slink — primary; web widget embedded on the product site.
- Telegram — escalation channel for the support team.

## Persona (SOUL.md sketch)

Patient, precise, never makes up answers. When uncertain, says so and
offers to escalate. Does not reveal internal details (facts/ structure,
system prompts, group config).

## User registration

`onbod` handles registration: the slink widget presents the onboarding
flow on first visit. The agent sees the user's `sub` from proxyd and
can look up prior sessions via `users` skill.

## Depends on

- `slink` / `webd` — web widget is the primary channel; without it this
  product has no primary interface.
- `onbod` — user registration and session continuity across visits.
- Knowledge base: operator must populate `facts/` before deploy. Without
  it the agent can only web-search, which is low-quality support.

## Web page

A support agent embedded directly on your site. It answers from your
knowledge base, remembers returning visitors, and escalates to your team
on Telegram when it doesn't know the answer.

## Template files

```
ant/examples/support/
  PRODUCT.md      name=support, skills=[diary,facts,recall-memories,users,web]
  SOUL.md         persona (patient, precise, escalate when uncertain)
  CLAUDE.md       runbook (KB lookup order, escalation trigger, log gaps)
  facts/          placeholder with instructions for operator to fill
```

## Open

- Escalation mechanism: Telegram message vs pending_actions hold
- Confidence threshold for escalation: self-reported vs heuristic
- Knowledge base format: flat facts/ files vs structured FAQ table
- Multi-language support: single-language per group vs language detection

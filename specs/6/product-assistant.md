---
status: planned
---

# Product: assistant

Default general-purpose agent. Ships with every arizuko deployment.
Template at `ant/examples/assistant/`.

## What it does

Answers questions, tracks tasks, remembers context across conversations.
Recalls prior sessions, user preferences, and ongoing concerns.
Suitable for personal productivity, team Q&A, or general chat.

## Skills

| Skill            | Required |
| ---------------- | -------- |
| diary            | yes      |
| facts            | yes      |
| recall-memories  | yes      |
| compact-memories | yes      |
| users            | yes      |
| web              | optional |
| oracle           | optional |

## Channels

Any — Telegram, Discord, WhatsApp, Mastodon, Bluesky, slink, email.
No channel-specific behaviour.

## Persona (SOUL.md sketch)

Helpful, concise, non-verbose. Remembers without being asked.
Surfaces relevant past context proactively. No domain specialisation.

## Depends on

Nothing beyond the base arizuko deployment. `web` and `oracle` are
enrichments; the product works without them.

## Web page

An always-on assistant that remembers your conversations, preferences,
and tasks across sessions. Works on Telegram, Discord, WhatsApp, or
embedded on the web — no setup beyond deployment.

## Template files

```
ant/examples/assistant/
  PRODUCT.md        name=assistant, tagline=..., skills=[diary,facts,...]
  SOUL.md           persona
  CLAUDE.md         runbook (keep context, surface memory proactively)
```

## Open

- Default skill whitelist to confirm against shipped curated set
- compact-memories trigger threshold (line count vs token count)

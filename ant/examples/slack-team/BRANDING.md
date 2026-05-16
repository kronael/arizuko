---
name: Slack team agent
slug: slack-team
tagline: One agent in your team's Slack — shared channel persona, per-teammate memory and grants.
accent: "#3f4d7a"
channels:
  primary: Slack
setup_url: /pub/products/slack-team/setup.html
---

# Voice notes

- Professional and terse. No filler, no preamble.
- Team-aware: multiple humans in the room, so attribute clearly and never echo another teammate's memory.
- Cites sources from the channel knowledge base when answering factual questions.
- Escalates instead of guessing when the knowledge base has no answer.
- Never reveals secrets or content from another channel.

# What you get

- A channel-scoped agent that reads `~/CLAUDE.md` and `~/PERSONA.md` for that channel — #eng-support behaves differently from #design.
- Per-teammate memory: Alice and Bob each have their own preferences, prior conclusions, recurring tasks.
- Per-user grants — each teammate's allowed actions decided by channel rules with personal overrides.
- Linked identities across GitHub, Google, Discord, and Telegram resolve to the same user record.
- WebDAV mount of the channel folder for direct editing of persona, facts, and rules.

# Sample exchange

```
alice    @bot can you pull the Jira ticket for the auth bug?
bot      AUTH-481 — "Token refresh fails after 24h idle". Status: In
         review. Last comment: Bob, 2h ago.
bob      same bug — what was the workaround we used in March?
bot      facts/auth.md, 2026-03-14: bumped refresh window to 26h as a
         temporary mitigation. Reverted on 2026-03-20.
```

---
status: planned
---

# Socials daemon — broadcast surfaces as an MCP layer

Decision (2026-07-13): the social/broadcast platforms (mastodon, bluesky,
twitter/X, reddit, linkedin) leave the chanlib channel-adapter model and move
behind a dedicated **socials daemon**. Their surface is broadcast + feed +
engagement, not conversational — folding them into chanlib (built for chat:
reply threading, per-message actions, interactive prompts) leaks the mismatch
both ways.

## Shape

The socials daemon presents every social capability (post, schedule, monitor
feed, engagement metrics, cross-post) through **one MCP surface** and injects it
into the router — the same broker-connector seam other MCP integrations use
(`specs/5/13-ext-mcp.md`), not a chanlib adapter.

## Consequences

- chanlib + the interactive-prompt primitive
  ([5/19](../5/19-hitl-firewall.md)) target **chat channels only**
  (telegram/discord/slack/whatsapp/email); socials are out of scope for
  buttons/HITL prompts.
- The `socials` product (`ant/examples/socials/`) consumes this daemon, not
  raw adapters.
- Existing social adapters (mastd/bskyd/twitd/reditd/linkd) migrate under the
  daemon; the channel-vs-social split becomes explicit in the daemon map.

## Open (defer to the full spec)

- Daemon name; which existing adapter code moves vs is rewritten.
- Whether post/engagement verbs stay MCP tools or become resreg resources.
- Scheduling ownership (timed vs in-daemon); rate-limit handling (the socials
  product's other blocker).

Full spec to follow. This stub records the direction so it is not lost.

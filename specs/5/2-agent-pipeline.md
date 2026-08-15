---
status: shipped
---

# Agent orchestration & workflows

**Decided: neither shape needs new machinery, so none was built.**

- **Orchestration** — long-lived groups messaging each other, each with
  its own session and memory. Built from the existing chat/route-token
  surface ([`W-webhook-routes.md`](W-webhook-routes.md), which renamed
  slink) plus `send_message`, with skill files driving the topology. No
  routd change.
- **Workflows** — one group spawning subagents through the Claude Code
  Agent tool inside a single container, sharing context. Already works;
  nothing to add.

Revisited for _declarative_ flows by
[`6/18-durable-workflows-decline.md`](../6/18-durable-workflows-decline.md),
which reaffirms the same call: workflow stays an operating discipline, not
a seventh primitive.

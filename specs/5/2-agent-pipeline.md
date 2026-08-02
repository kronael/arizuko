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

Superseded for _declarative_ flows by
[../13/6-workflows.md](22-self-learning.md).

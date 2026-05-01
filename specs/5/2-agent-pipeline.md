---
status: shipped
---

> Shipped: both shapes rely only on existing primitives. Orchestration
> uses slink (`1/W-slink.md`) + `send_message`. Workflows use the Claude
> Code Agent tool inside a container. No gateway work outstanding;
> declarative workflow syntax tracked separately in `6/6-workflows.md`.

# Agent orchestration & workflows

Two shapes:

- **Orchestration** — long-lived groups messaging each other via slink.
  Own session/memory.
- **Workflows** — single group spawns subagents via Agent tool in one
  container. Shared context.

Rationale: workflows already work via Claude Code Agent tool. No gateway
changes needed. Orchestration = existing slink + `send_message` + skill
files driving the topology.

Superseded for declarative flows by [../6/6-workflows.md](../6/6-workflows.md).

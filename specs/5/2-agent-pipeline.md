---
status: partial
---

# Agent orchestration & workflows

Two shapes:

- **Orchestration** — long-lived groups messaging each other via slink.
  Own session/memory. Works today once slink is shipped.
- **Workflows** — single group spawns subagents via Agent tool in one
  container. Shared context. Works today.

Rationale: workflows already work via Claude Code Agent tool. No gateway
changes needed. Orchestration = existing slink + `send_message` + skill
files driving the topology.

Superseded for declarative flows by [../6/6-workflows.md](../6/6-workflows.md).

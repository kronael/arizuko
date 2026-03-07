# v2: Audience Agents

v2 is about agents that serve many users — not just the operator.

The generic patterns live here. Product-specific specs (atlas/REDACTED)
live in `specs/atlas/` where they can be thought through independently
and later modularized into reusable components.

## What moved

Previous v2 specs (topics, memory layers, IPC→MCP, workflows,
channel adapters) moved to v3/. They're infrastructure concerns.

## Atlas specs (specs/atlas/)

- facts.md — knowledge base format and injection
- facts-search.md — similarity search over facts
- researcher.md — background research tasks
- verifier.md — quality gate on research
- user-context.md — per-user memory files
- capabilities.md — evangelist plugin mapping
- setup.md — migration from ElizaOS
- instance-repos.md — instance configs as git repos

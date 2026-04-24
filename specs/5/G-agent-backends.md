---
status: rejected
---

# Alternative agent backends — not shipping

> Rejected 2026-04-24: Staying on Claude Agent SDK. Codex/Pi adoption
> not pursued given SDK maturity + MCP integration.

Evaluated Codex CLI and pi-coding-agent as ant backends behind an
`AgentDriver` interface (`run`, `steer`, `abort`, `sessionId`) with
`AGENT_BACKEND=claude|codex|pi`.

Decision: stay on Claude Agent SDK.

- Claude SDK: native hooks, `PostToolUse` for mid-loop injection, MCP.
- Codex CLI: one-shot `codex exec --json` — mid-turn IPC injection
  requires respawn per follow-up. Breaks core arizuko pattern.
- Pi RPC: `steer` is first-class mid-turn primitive; multi-provider.
  Needs custom MCP extension; solo-dev maturity risk.

Revisit if SDK deprecated, GPT/Gemini agent requested, or pi gains a
stable MCP extension ecosystem.

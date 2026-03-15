## <!-- trimmed 2026-03-15: TS removed, rich facts only -->

## status: spec

# Code Researcher

Product config: group + skill that turns an instance into a codebase
Q&A assistant. Users ask questions; agent researches and replies.

## How It Works

```
User question → gateway → container agent
  → /workspace/codebase (ro mount of target repo)
  → Agent reads/greps/globs with built-in CC tools
  → Streams findings back as text reply
```

No separate research queue. Long jobs stream interim updates via
`send_message` IPC.

## Config

```env
CODEBASE_PATH=/path/to/target/repo   # host path
CODEBASE_NAME=REDACTED               # label for agent prompt
```

Gateway appends ro mount: `CODEBASE_PATH → /workspace/codebase`.

## Skill: `code-researcher`

`container/skills/code-researcher/SKILL.md` instructs the agent:

- Primary role: answer questions about `/workspace/codebase`
- Use Read, Grep, Glob, Bash(ls/find/tree/cat/head/wc)
- No write tools or arbitrary commands on codebase
- Long research: `send_message` with interim findings, then summary
- Cite file paths and line numbers
- Clarify vague questions before researching

## Out of Scope (v1)

- Facts/knowledge base accumulation
- Feedback collection, tickets, escalation
- Multi-codebase support (one mount per instance)
- Read-only tool enforcement at gateway level (skill convention only)

---
status: unshipped
---

# Code Researcher

Product config: group + skill that turns an instance into a codebase
Q&A assistant. Users ask, agent researches and replies.

## Flow

```
User question → gateway → container agent
  → /workspace/codebase (ro mount of target repo)
  → Read/Grep/Glob/Bash(ls/find/tree/cat/head/wc)
  → text reply, optional interim send_message
```

No separate research queue. Long jobs stream interim updates via
`send_message` IPC.

## Config

```env
CODEBASE_PATH=/path/to/target/repo   # host path
CODEBASE_NAME=<label>                # label for agent prompt
```

Gateway appends ro mount: `CODEBASE_PATH → /workspace/codebase`.

## Skill

`ant/skills/code-researcher/SKILL.md` instructs: answer questions about
`/workspace/codebase`; no write tools; long research = interim
`send_message` + summary; cite file paths and line numbers; clarify vague
questions before researching.

## Out of scope (v1)

- Facts/knowledge base accumulation
- Feedback collection, tickets, escalation
- Multi-codebase (one mount per instance)
- Read-only enforcement at gateway (skill convention only)

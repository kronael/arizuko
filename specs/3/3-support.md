---
status: spec
---

# Code Researcher

A kanipi product configuration that turns a group into a codebase Q&A
assistant. Users ask questions about a mounted codebase; the agent researches
deeply and replies with findings.

Ported from `eliza-atlas/packages/eliza-plugin-evangelist` (ResearchService +
CodebaseService + ClaudeCodeService pipeline). In kanipi the heavy lifting
moves into the agent container — no separate service layer needed.

---

## Problem

Eliza-atlas runs the research pipeline as a Node service (ElizaOS runtime +
Claude Code CLI subprocess). Kanipi already runs Claude Code inside a
container with access to a mounted codebase. The goal is a clean product
config — a group + skill that makes a kanipi instance behave like eliza-atlas's
REDACTED Atlas bot.

---

## How it works

```
User question (Telegram/Discord/etc.)
         ↓
kanipi gateway → container agent
         ↓
/workspace/codebase (ro mount of target repo)
         ↓
Agent reads files, greps, searches with built-in CC tools
         ↓
Streams findings back as text reply
```

No separate research queue. The agent uses its native tool access (Read, Grep,
Glob, Bash(ls/find/tree)) directly on the mounted codebase. Long research
jobs stream interim updates via `send_message` IPC.

---

## Kanipi changes required

### 1. Extra mount config (already partially specced in `specs/1/D-files-in.md`)

Instance `.env` gains:

```env
CODEBASE_PATH=/path/to/target/repo   # host path
CODEBASE_NAME=REDACTED               # label used in agent prompt
```

Gateway reads these and appends an extra mount:

```
hostPath: CODEBASE_PATH  →  containerPath: /workspace/codebase  (readonly)
```

This is the only gateway change needed. The rest is skill/CLAUDE.md config.

### 2. Agent skill: `code-researcher`

`container/skills/code-researcher/SKILL.md` — instructs the agent:

- Your primary role is answering questions about the codebase at
  `/workspace/codebase`.
- Use Read, Grep, Glob, Bash(ls/find/tree/cat/head/wc) to explore.
- Do NOT use write tools or run arbitrary commands on the codebase.
- For long research: call `send_message` with interim findings, then
  reply with a summary when done.
- Cite file paths and line numbers in answers.
- If question is vague, ask for clarification before researching.

### 3. Extra mount support in gateway

`src/container-runner.ts` needs to read `EXTRA_MOUNTS` from config
(or per-call extra mounts) and append them to `buildVolumeMounts`.

`src/config.ts`:

```ts
export const CODEBASE_PATH = process.env.CODEBASE_PATH || '';
export const CODEBASE_NAME = process.env.CODEBASE_NAME || 'codebase';
```

`src/container-runner.ts` — in `buildVolumeMounts`:

```ts
if (CODEBASE_PATH) {
  mounts.push({
    hostPath: CODEBASE_PATH,
    containerPath: '/workspace/codebase',
    readonly: true,
  });
}
```

---

## What eliza-atlas has that kanipi does NOT need to port

| Eliza-atlas component                 | Kanipi equivalent                       | Needed?   |
| ------------------------------------- | --------------------------------------- | --------- |
| ClaudeCodeService (subprocess mgmt)   | container-runner already does this      | No        |
| ResearchService queue + poll loop     | agent handles sequentially in container | No        |
| CodebaseService (file search helpers) | agent uses Read/Grep/Glob natively      | No        |
| FactsService (fact extraction)        | agent's own reasoning                   | No        |
| KnowledgeDiscoveryService             | out of scope v1                         | No        |
| HelpSessionManager                    | kanipi groups already scope sessions    | No        |
| researchNeededAction (classification) | single agent decides inline             | No        |
| Auth bridge (OAuth token mgmt)        | gateway runs outside container          | No        |
| ALLOWED_TOOLS restriction             | skill CLAUDE.md rule (no write tools)   | Via skill |
| Interim delivery via handleMessage    | `send_message` IPC                      | Via skill |

---

## What needs to be built

1. **Extra mount in gateway** — `CODEBASE_PATH` env → ro mount at
   `/workspace/codebase`. ~20 lines in `config.ts` + `container-runner.ts`.

2. **`code-researcher` skill** — `container/skills/code-researcher/SKILL.md`
   with research instructions, tool restrictions, interim messaging convention.

3. **Instance setup doc** — how to create a code-researcher kanipi instance
   (which env vars, which groups, skill enabled by default).

---

## Out of scope (v1)

- Facts/knowledge base accumulation (FactsService)
- Feedback collection (FeedbackService)
- Ticket/escalation helpers
- Stats and session management commands
- Multi-codebase support (one mount per instance)
- Read-only tool enforcement at gateway level (skill convention only)

---

## Deployment sketch

```bash
kanipi create REDACTED
# edit /srv/data/REDACTED/.env:
#   CODEBASE_PATH=/srv/data/REDACTED-codebase
#   CODEBASE_NAME=REDACTED
#   TELEGRAM_BOT_TOKEN=...
#   ASSISTANT_NAME=Atlas
sudo systemctl enable --now REDACTED
```

Agent container sees `/workspace/codebase` as the target repo. Skill
instructions in `~/.claude/skills/code-researcher/SKILL.md` guide behavior.
CLAUDE.md rule enforces `send_file` and `send_message` for delivery.

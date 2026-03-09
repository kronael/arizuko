# Memory: Diary — shipping

Agent-written daily notes. The agent decides what goes in —
content is subjective, freeform. The primary human-readable
record of what happened in a group over time.

## Path

```
groups/<folder>/diary/YYYYMMDD.md
```

Mounted rw at `/workspace/group/diary/` inside the container.

## File format

```markdown
---
summary: |
  Working on nanoclaw gateway. Alice is the main user.
  - auth: OAuth flow design, provider TBD
  - deploy: REDACTED Ansible config done
  - ipc: two file-sending bugs open
---

## 10:32

Helped Alice configure Ansible for REDACTED. Vault password
path was wrong — fixed. deploy: done.

## 14:07

Auth flow discussion. Alice wants OAuth not passwords.
auth: provider TBD. New task — ipc: file sending broken,
ENOENT on sendDocument.
```

- `summary:` — YAML block scalar. Keep it short. First line:
  project and who you work with. Then up to 5 bullet points
  of clearly important tasks only — if you're unsure whether
  something belongs, leave it out. Gateway reads only this
  field for session-start injection.
- `## HH:MM` entries — 250 chars max each. Entries naturally
  introduce and update tasks (e.g., "New task — auth: OAuth
  flow design", "auth: decided on GitHub provider",
  "deploy: done"). Tasks appear and change state through
  entries — no separate tracking. The diary IS the task log.
- Agent may rewrite/compress old entries to save space
- Truthful, summarizing — only what matters
- If nothing noteworthy, skip

## Triggers

### 1. `/diary` skill (agent-initiated)

Agent can run `/diary` anytime during a session. The skill
instructs the agent to append to `/workspace/group/diary/YYYYMMDD.md`.

### 2. PreCompact hook (automatic)

On compaction, return `{ systemMessage: nudge }` where nudge
is the skill's `description` frontmatter field:

```
If anything worth noting happened since your last diary
entry, record it in /workspace/group/diary/YYYYMMDD.md.
```

Agent decides whether to act. Not a command — a nudge.
Replaces the current `createPreCompactHook` transcript dump.
Resets the turn counter (see below).

### 3. Stop hook turn nudge (automatic, every 100 turns)

Agent-runner already tracks `messageCount`. On Stop hook:

```ts
if (messageCount >= 100 && !input.stop_hook_active) {
  messageCount = 0;
  return { systemMessage: nudgeText };
}
```

- `nudgeText` is the skill's `description` frontmatter
  (same text as PreCompact — single source of truth)
- `stop_hook_active` guard prevents infinite loops (the
  Stop hook fires again after the nudge-triggered response)
- Counter resets on nudge; PreCompact also resets it
- No gateway involvement — entirely in agent-runner

## Gateway: session-start injection

On session reset, gateway reads the two most recent diary
files' YAML frontmatter and injects system messages with
relative time ("today", "yesterday", "3 days ago"):

```
[diary, today] <summary text>
[diary, 3 days ago] <summary text>
```

Construction (no API call):

1. List `groups/<folder>/diary/*.md`, sort descending, take 2
2. Read `summary:` from frontmatter of each file
3. Compute age from filename date vs now (e.g. "today",
   "yesterday", "3 days ago")
4. Inject as system messages before conversation XML

No diary = no injection (cold start with MEMORY.md only).
Single file = one message. Two files = two messages.

## Gateway: mount

`container-runner.ts` adds:

```
groups/<folder>/diary/ → /workspace/group/diary/ (rw)
```

Create dir if missing (`mkdirSync recursive`).

## What gets deleted

- `createPreCompactHook` — transcript dump (replaced by nudge)
- `parseTranscript`, `formatTranscriptMarkdown`,
  `sanitizeFilename`, `generateFallbackName` — dead code
- `conversations/` directory — no longer created

## Implementation

| Component  | File                                  | Change                              |
| ---------- | ------------------------------------- | ----------------------------------- |
| Mount      | `src/container-runner.ts`             | Add diary dir mount                 |
| Injection  | `src/index.ts`                        | Read frontmatter, inject system msg |
| PreCompact | `container/agent-runner/src/index.ts` | Replace transcript dump with nudge  |
| Stop hook  | `container/agent-runner/src/index.ts` | Turn counter nudge at 100           |
| Skill      | `container/skills/diary/SKILL.md`     | Writing rules + nudge text          |
| Migration  | `container/skills/self/migrations/`   | Skill file delivery                 |

## Relationship to other memory layers

| Layer                | Trigger        | Granularity           |
| -------------------- | -------------- | --------------------- |
| Diary (this)         | Agent + hooks  | Per-session, daily    |
| Episodes (v2)        | Scheduled task | Weekly/monthly        |
| Long-term/facts (v2) | Episode rollup | Permanent, conceptual |

Diary is the raw input. Episodes aggregate upward.
Long-term distills recurring concepts from episodes.

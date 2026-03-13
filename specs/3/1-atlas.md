---
status: shipped
---

# Atlas: What We Actually Need

The evangelist plugin's real value stripped of ElizaOS scaffolding.

## 1. Facts

YAML markdown knowledge files. Institutional memory.

**Status:** Shipped. 85+ files live in `groups/main/facts/`.
Schema: `path`, `category`, `topic`, `verified_at`, `header` (dense summary).

## 2. Facts retrieval + researcher + verifier

`/facts` skill handles the full cycle:

- Retrieval: Explore subagent scans `header:` fields to find relevant files
- Research: subagent searches web + codebase, writes new/updated fact files
- Verify: second subagent pass cross-checks and stamps `verified_at`
- Age gate: facts older than 14 days are refreshed, not discarded

**Status:** Shipped (`container/skills/facts/SKILL.md`).

**Deferred (v2):** Semantic similarity search (embeddings), automatic
injection into every prompt, background researcher cron.

## 5. Persona / gatekeeper

How the agent behaves, who it responds to, honesty rules.

**Status:** Done. CLAUDE.md + character.json + group trigger mode.

## Also missing (not evangelist-specific, but needed)

### Forwarded messages — shipped (v0.7.0)

Telegram and WhatsApp extract `forward_origin` metadata and store
`forwarded_from` on the message row. `formatMessages()` emits
`<forwarded_from sender="..."/>` XML in the prompt.

### Reply-to threading — shipped (v0.7.0)

Channels extract reply context (`reply_to_text`, `reply_to_sender`)
and store on the message row. `formatMessages()` emits
`<reply_to sender="...">text</reply_to>` XML in the prompt.

## Sandboxed support (product pattern)

Public-facing agent with restricted permissions using the tier model:

```
atlas/               → tier 1: world admin
  atlas/support      → tier 2: research backend (rw facts/)
    atlas/support/web → tier 3: user-facing (ro, escalate-only)
```

Worker escalates to parent when facts insufficient. This is product
configuration, not new gateway code — depends on `specs/2/5-permissions.md`.

## Deferred (v2)

- Semantic similarity search (embeddings)
- Automatic injection into every prompt
- Background researcher cron

---
name: facts
description:
  Research a topic, collect facts from conversation, or refresh stale facts.
  Use when facts/ has no matches, when asked to research something, or after a long
  conversation to extract and persist what was learned.
user_invocable: true
arg: <question or topic, or "collect" to extract from recent context>
---

# Facts

Two modes: **research** (find and verify external knowledge) and **collect**
(extract facts from the current conversation or recent diary).

## Mode: collect

When arg is `collect` or no arg given after a long session:

Spawn a single subagent that:

1. Reads the last diary entry (`diary/YYYYMMDD.md`)
2. Reads recent messages from context
3. Identifies new facts worth persisting: about users, systems, decisions, entities
4. For each fact: check if `facts/<slug>.md` already exists
   - If yes: append new knowledge, update `verified_at`
   - If no: create with YAML frontmatter (see format below)
5. Reports: N facts updated, M facts created

Do NOT collect trivial or ephemeral facts. Facts should answer
"what do we permanently know about X?" — not "what happened today?".

## Mode: research

When arg is a question or topic:

### Step 1: Research (subagent)

Spawn a research subagent. It must:

- Search existing `facts/` for related knowledge (`/recall-memories <topic>`)
- Search the web (WebSearch, WebFetch)
- Write new fact files to `facts/` with YAML frontmatter
- One fact per file, named by topic slug
- Stop after 3–10 new facts

### Step 2: Verify (subagent per batch)

For each batch of new facts (max 5), spawn a verifier subagent:

- Cross-reference against codebase and web sources
- Check for contradictions with existing facts
- Delete facts that fail verification
- Update `verified_at` on passing facts

### Step 3: Answer

Read surviving fact files, answer the user's original question naturally.

## Fact file format

```yaml
---
topic: <specific topic>
category: <top-level category>
verified_at: <ISO timestamp>
summary: >
  <one sentence — used by /recall-memories for fast grep>
---
<full content: explanation, sources, code refs>
```

## Rules

- ALWAYS use subagents — never research in main context
- NEVER delete existing facts in collect mode — only append or create
- Research subagent tools: Read, Glob, Grep, WebSearch, WebFetch, Write
- Verifier subagent tools: Read, Glob, Grep, WebSearch, WebFetch, Bash

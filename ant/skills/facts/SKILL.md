---
name: facts
description: Research a topic and produce verified facts in facts/. Use when
  /recall-memories finds no match or when asked to research something.
user-invocable: true
arg: <question or topic to research>
---

# Facts

Research → verify → write to `facts/` for future recall via `/recall-memories`.
ALWAYS use subagents — never research in main context.

## Step 1: Research (subagent)

Tools: Read, Glob, Grep, WebSearch, WebFetch, Write.

- Search existing `facts/` for related knowledge first
- Search the web
- Write new fact files to `facts/` with YAML frontmatter:
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
- One fact per file, named by topic slug
- Stop after 3–10 new facts

## Step 2: Verify (subagent per batch of 5)

Tools: Read, Glob, Grep, WebSearch, WebFetch, Bash.

- Cross-reference against codebase and web sources
- Check for contradictions with existing facts
- Delete facts that fail verification
- Update `verified_at` on passing facts

## Step 3: Answer

Read surviving fact files, answer the user's original question naturally.

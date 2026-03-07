---
name: facts
description: Research a topic and produce verified facts. Spawns
  subagents to search, write, and verify in steps. Use when facts/
  has no matches for a question or when asked to research something.
user_invocable: true
arg: <question or topic to research>
---

# Facts Research

Produce verified facts on a topic. All work in subagents to keep
the main context clean.

## Step 1: Research (subagent)

Spawn a research subagent with the topic. It must:

- Search existing facts/ for related knowledge
- Search codebase refs in /workspace/extra/ (Grep, Read)
- Search the web (WebSearch, WebFetch)
- Write new fact files to facts/ with YAML frontmatter:
  ```yaml
  ---
  path: <slug>
  category: <top-level category>
  topic: <specific topic>
  verified_at: <ISO timestamp>
  header: >
    <one-paragraph summary>
  ---
  <full content with sources, code refs, explanations>
  ```
- One fact per file, named by topic slug
- Do NOT edit or delete existing facts
- Stop after 3-10 new facts

## Step 2: Verify (subagent per batch)

For each batch of new facts (max 5 per batch), spawn a verifier
subagent. It must:

- Read each new fact file
- Cross-reference against codebase and web sources
- Check for contradictions with existing facts
- Check that cited sources support the claims
- Delete facts that fail verification
- Update verified_at on facts that pass

## Step 3: Answer

Read the surviving fact files, then answer the user's original
question using them as knowledge. Don't report the research
process — just answer naturally as if you always knew.

## Rules

- ALWAYS use subagents — never research in main context
- Batch verification: max 5 facts per verifier subagent
- Research subagent tools: Read, Glob, Grep, WebSearch, WebFetch, Write
- Verifier subagent tools: Read, Glob, Grep, WebSearch, WebFetch, Bash

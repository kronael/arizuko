---
status: planned
---

# Researcher

Background task that explores repos, web, docs — writes findings to facts/.

## Evangelist approach

- 40-min Opus research task
- Triggered by knowledge gap or explicit `/research <question>`
- Read-only tools: Grep, Glob, Read, WebSearch, WebFetch, git clone
- Output: XML factset → parsed into facts/\*.md files
- Two-phase: Opus researches, Sonnet verifies (rejects bad findings)
- Results delivered back to conversation via reply threading

## Kanipi approach

Subagent spawning. The agent already has all tools natively.

### Trigger (v1)

Agent detects knowledge gap via grep (no embedding infrastructure).
Future: similarity threshold when embeddings ship.

- No facts match (grep returns nothing)
- Or user asks explicitly (`/research <question>`)
- Agent spawns a research subagent

### Subagent

- Gets: question, facts/ (read), refs/codebase/ (read), web search
- Does: search code, read docs, web research
- Writes: new facts file(s) to facts/ with YAML frontmatter
- Returns: summary of findings to parent

### Delivery

- Parent agent incorporates findings into response
- Or: if research takes >30s, tell user "researching..." and
  deliver results on next message (needs reply threading — v1 spec)

## Open questions

- Timeout? 5 min? 10 min? 40 min was too long for user experience
- Should subagent write directly to facts/ or return to parent?
- How to prevent duplicate research on same topic?
- Git clone into where? Container-local or persistent refs/?
- Quality control without formal verifier?

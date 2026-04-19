---
status: unshipped
---

# Researcher

Background task that explores repos, web, docs — writes findings to
`facts/`.

## Arizuko approach

Subagent spawning. Parent already has all tools natively.

### Trigger (v1)

Agent detects knowledge gap via grep (no embeddings yet):

- No facts match, or
- User asks explicitly (`/research <question>`)

Agent spawns a research subagent.

### Subagent

- Gets: question, `facts/` (read), `refs/codebase/` (read), web search
- Does: search code, read docs, web research
- Writes: new `facts/*.md` with YAML frontmatter
- Returns: summary to parent

### Delivery

Parent incorporates into response. If >30s, reply "researching…" and
deliver results on next message (needs reply threading).

## Evangelist reference

40-min Opus task, read-only tools, two-phase (Opus researches, Sonnet
verifies), XML factset → parsed into files. Arizuko: native subagent
instead of external process.

## Open

- Timeout (5 / 10 / 40 min — 40 was too long for UX)
- Subagent writes directly vs returns to parent
- Prevent duplicate research on same topic
- Git clone location (container-local or persistent refs/)
- Quality control without formal verifier

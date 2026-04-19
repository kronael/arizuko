---
status: deferred
---

# Memory: Facts / Long-term

Concept-centric persistent knowledge. Depends on atlas design.

## What it is

Long-term memory organised by concept/entity rather than time. Diary
asks "what happened today?", facts ask "what do we know about Alice?".

Distilled from diary/episodes by agent or scheduled extraction.

```
groups/<folder>/facts/
  alice.md          everything known about Alice
  auth-system.md    design decisions, open questions
```

## Relationship to MEMORY.md

Both are file-based, agent-written.

| File                 | Content                                | Updated by                 |
| -------------------- | -------------------------------------- | -------------------------- |
| `MEMORY.md`          | Tacit/behavioural — style, how-to-work | Agent, autonomously        |
| `facts/<concept>.md` | World facts — who/what                 | Agent or scheduled extract |

MEMORY.md = "how". Facts = "what".

## Push / pull

- Push: on reset, inject list of fact file names (not content), let
  agent read what it needs.
- Pull: agent reads `/workspace/group/facts/<concept>.md`. Optional
  `recall(subject)` / `search_facts(query)` MCP tools.

## Prior art

- **Martian-Engineering agent-memory** — bash/jq, atomic facts as JSON,
  Jaccard dedup, 30-day half-life scoring, entity index cascade.
- **eliza-atlas / evangelist** — reference implementation. Markdown
  with YAML frontmatter; pgvector semantic search; three-tier
  similarity; two-phase verification via Claude.
- **Claude Code MEMORY.md** — 200-line index + topic files, already
  live in containers.

Paths: `/home/onvos/app/eliza-plugin-evangelist/src/services/factsService.ts`.

## Open

- Agent-written (like diary) vs scheduled extraction
- Contradiction handling: last-write-wins vs mark historical
- Scope: per-group vs instance-wide (`/workspace/global/facts/`)
- Per-user privacy scoping
- Retention / pruning of stale facts
- Cross-ref with `specs/5/9-identities.md` — identity claims bootstrap
  user fact files

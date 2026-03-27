---
status: draft
---

# Memory: Facts / Long-term — open (v2)

**Status**: partial

Concept-centric persistent knowledge. Automatic, always on.
Not planned for v1 — depends on atlas system design.

## What it is

Long-term memory organised by concept/entity rather than by time.
While diary asks "what happened today?", facts ask "what do we know
about Alice?" or "what is the current state of the auth system?".

Facts are distilled from diary entries and episodes by the agent or by
a scheduled extraction process. They live in concept files, not time files.

```
groups/<folder>/facts/
  alice.md          ← everything known about Alice
  REDACTED.md         ← server config, deployment history
  auth-system.md    ← design decisions, open questions
  ...
```

## Relationship to MEMORY.md

MEMORY.md (Claude Code's built-in auto-memory) follows the same
file-based pattern — 200-line index loaded every session, with agent
offloading detail into topic files alongside it. The agent writes it
autonomously using standard file tools (no special MCP).

The distinction:

| File                 | Content                                             | Updated by                    |
| -------------------- | --------------------------------------------------- | ----------------------------- |
| `MEMORY.md`          | Tacit/behavioural — style, preferences, how to work | Agent, autonomously, any time |
| `facts/<concept>.md` | World facts — who Alice is, what REDACTED runs        | Agent or scheduled extraction |

MEMORY.md is for "how" knowledge. Facts are for "what" knowledge.
Agent is already instructed to use MEMORY.md this way by Claude Code's
built-in system prompt: _"When you notice a pattern worth preserving
across sessions, save it here."_

Facts files extend the same pattern into a dedicated concept-indexed
directory. Same mechanism (agent writes markdown files), different
organisation (by concept, not by behaviour).

## Push (auto-injected)

On session reset, gateway injects a list of fact file names (not content)
alongside the diary pointer, so the agent knows what concept knowledge
exists. Agent reads the files it needs.

Not yet designed in detail — depends on how many facts accumulate.

## Pull (on demand)

Agent reads fact files directly via file tools:

```
/workspace/group/facts/alice.md
/workspace/group/facts/REDACTED.md
```

MCP tools (from atlas system) may provide structured query:

```
recall(subject)       → returns facts/<subject>.md
search_facts(query)   → full-text search across facts/
```

## Prior art

**Martian-Engineering agent-memory** (closest structural match):

Bash-based, no external deps (bash + jq + uuidgen). Three layers:

- `memory/entities/<entity>/items.json` — atomic facts as JSON, never
  deleted; contradicted facts marked `historical` with supersession links
- `memory/entities/<entity>/summary.md` — weekly-updated entity snapshot
- `memory/YYYY-MM-DD.md` — daily notes (maps to our diary)
- `MEMORY.md` — tacit/operational patterns (maps to our managed memory)
- `memory/index.json` — entity index for fast lookup

Extraction pipeline (`add-fact-validated.sh`):

- Dedup: Jaccard similarity >70% rejected
- Contradiction: "Alice works at X" supersedes prior "Alice works at Y"
- Rejects transient states ("is tired") and vague language

Time decay (retrieval scoring): `e^(-λ × days_old)`, λ = ln(2)/30
→ 30-day half-life: fact aged 30 days scores 0.5, 90 days scores 0.125

Retrieval cascade: entity index → summary (top 5) → facts (top 10
active) → daily notes fallback (last 7 days)

Weekly synthesis: rewrites summaries, resolves contradictions, prunes
facts >6 months old (content truncated, not deleted), updates MEMORY.md
if new patterns emerge.

Reference: [github.com/Martian-Engineering/agent-memory](https://github.com/Martian-Engineering/agent-memory)

**eliza-atlas / eliza-plugin-evangelist** (our reference implementation):

The facts system in eliza-atlas is the closest to what we want to build.
Key design:

- `facts/<topic>.md` — markdown with YAML frontmatter per topic
- Frontmatter: `path`, `topic`, `verified_at`, `confidence` (high/medium/low),
  `findings_count`
- Facts are paragraphs (10–2000 chars) separated by blank lines
- Vector embeddings stored in PostgreSQL for semantic search
- Three-tier similarity ranking: primary ≥90%, context 70–90%, weak 40–70%
- Two-phase verification via Claude (disproval phase + assessment phase)
- Research triggered when fact match similarity < 90% (knowledge gap)
- Claude Code (Opus) spawned as subprocess to research and produce factsets

Provider injects matching facts as XML block into LLM prompt automatically.
No MCP — uses ElizaOS provider/action architecture instead.

Reference: `/home/onvos/app/eliza-plugin-evangelist/src/services/factsService.ts`

**ATLAS agent** (syahiidkamil):

- `IMPORTANT_NOTES.md` — critical lessons, decision history
- `specific/` — reusable conventions
- File-based, no external DB. "Living memory persists. Learning compounds."

**Claude Code MEMORY.md pattern**:

- 200-line index + topic files alongside (`debugging.md`, `patterns.md`)
- Agent writes autonomously, no special trigger
- Already live in our containers via `CLAUDE_CODE_DISABLE_AUTO_MEMORY=0`

## Open

- Design atlas system before implementing facts
- Decide: agent-written (like diary) vs automated extraction
  (like Martian-Engineering's 30-min extractor)
- Contradiction handling: last-write wins, or mark historical?
- Scope: per-group facts vs instance-wide (`/workspace/global/facts/`)
- Privacy: facts shared across all group users — per-user scoping needed
- Whether `facts/` lives alongside `diary/` and `episodes/` or separately
- Retention/pruning policy for stale facts
- Cross-reference: `specs/3/9-identities.md` — identity claims are a
  natural bootstrap for user fact files

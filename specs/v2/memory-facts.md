# Memory: Facts / Long-term — open (v2)

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

**Martian-Engineering agent-memory** (closest match):

- `memory/entities/<entity>/` — atomic facts as JSON, entity `summary.md`
- `memory/YYYY-MM-DD.md` — daily notes (our diary)
- `MEMORY.md` — tacit/behavioural knowledge
- Automated extraction every 30 min, contradiction detection,
  weekly synthesis, exponential time decay (30-day half-life)

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
- Cross-reference: `specs/v2/identities.md` — identity claims are a
  natural bootstrap for user fact files

# Memory: Facts / Long-term — open (v2)

Concept-centric persistent knowledge. Automatic, always on.
Depends on atlas system. Not planned for v1.

## What it is

Long-term memory organised by concept/entity rather than by time.
Where episodes ask "what happened on week 9?", facts ask "what do we
know about Alice?" or "what is the current state of the auth system?".

Facts are distilled from episodes by the agent, not extracted mechanically.
The agent decides what is worth promoting to a concept file.

```
groups/<folder>/facts/
  alice.md          ← everything known about Alice
  REDACTED.md         ← server config, deployment history
  auth-system.md    ← design decisions, open questions
  ...
```

Each file is a living document — agent appends and revises as new
information arrives via episodes.

## Push (auto-injected)

On session reset, the gateway could inject a list of fact file names
(not content) so the agent knows what concept knowledge exists. Not yet
designed.

## Pull (on demand)

Agent reads fact files directly via file tools. MCP tools (from atlas
system) provide structured query:

```
recall(subject)         → returns facts/subject.md
search_facts(query)     → full-text or semantic search across facts/
```

## Relationship to other layers

- **Input**: fed by episodes (`specs/v2/memory-episodic.md`), also by
  direct agent observation during conversation
- **Atlas system**: the facts MCP tools and storage backend will be
  defined by the atlas system — this spec defers to that design
- **Identities**: user identity claims (from `specs/v1/auth.md` and
  `specs/v2/identities.md`) are a natural source of facts
  (e.g. Alice's timezone, preferred language)

## Prior art

- **Muaddib**: chronicles retain "tone of voice", "emotional charge",
  short quotes — narrative rather than structured facts. Privacy concern:
  unencrypted, shared across all channel users.
- **mem0 / Cognee / Recall**: external services for persistent memory
  with vector search and knowledge graphs. We prefer local-first.
- **brainpro**: `MEMORY.md` is the closest equivalent — freeform prose,
  not concept-indexed. We want concept-centric files instead.

## Open

- Design atlas system before implementing
- Define fact file format — freeform markdown vs structured YAML
- Aggregation trigger: when does the agent extract facts from episodes?
  (scheduled task, or agent-initiated during conversation?)
- Scope: per-group facts vs instance-wide facts (global facts.db in
  `/workspace/global/` for non-main groups, read-only)
- Privacy: fact files are shared across all users in a group — needs
  explicit scoping for per-user facts
- Whether `facts/` and `episodes/` should be in the same directory
  or kept separate
- Vector search vs full-text vs filename-only for `search_facts`

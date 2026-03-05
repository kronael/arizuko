# Memory: Facts — open

Structured, queryable facts about users, entities, and context.

## Current state

Nothing exists. Facts are either:

- In CLAUDE.md (unstructured, agent-managed, freeform text)
- Implicitly in conversation history (buried, not queryable)
- Nowhere (lost after context window)

## Problem

CLAUDE.md is a good scratchpad but a bad fact store. The agent can't
efficiently answer "what is Alice's timezone?" without reading the whole
file. Facts get stale, duplicated, or lost in prose.

## Proposed

A structured fact store per group, readable and writable by the agent
via MCP tools.

### Storage

SQLite table in the group's local DB (or a separate
`/workspace/group/facts.db`):

```sql
CREATE TABLE facts (
  id         INTEGER PRIMARY KEY,
  subject    TEXT NOT NULL,   -- "alice", "this group", "deployment"
  predicate  TEXT NOT NULL,   -- "timezone", "prefers", "host"
  object     TEXT NOT NULL,   -- "UTC+2", "bullet points", "REDACTED"
  confidence TEXT,            -- "stated", "inferred"
  source     TEXT,            -- "alice said 2026-03-05"
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX ON facts(subject, predicate);
```

Subject/predicate/object — simple triple store. Last-write wins per
`(subject, predicate)`.

### MCP tools

```
remember_fact(subject, predicate, object, source?)
forget_fact(subject, predicate)
recall_facts(subject?)   → returns all facts for subject
```

Agent calls these explicitly when it learns something worth keeping.
No automatic extraction — agent decides what is a fact.

### Scope

- Per-group facts live in group workspace
- Global facts (about the instance, about the operator) live in
  `/workspace/global/facts.db` (read-only for non-main groups)

## Relationship to CLAUDE.md

Facts DB is for structured, queryable data. CLAUDE.md remains for
behavioural context, style notes, and prose that doesn't fit triples.
The agent should use facts DB for "what is X" and CLAUDE.md for
"how should I behave".

## Open

- Implement `facts.db` schema and MCP tools
- Decide: group-local SQLite vs flat JSON vs CLAUDE.md sections
- Auto-populate from identity claims when identities spec ships
  (e.g. `remember_fact("alice", "telegram_id", "tg:123456")`)

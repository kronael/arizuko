---
status: shipped
---

# Recall — Knowledge Retrieval

Generic search across knowledge stores. Read-only — never writes.
All stores use `summary:` frontmatter, so recall treats them
identically. A store is just a directory name.

## Stores

```
facts, diary, users, episodes
```

Each store is a directory of `*.md` files with `summary:` in YAML
frontmatter. Adding a store = one string. No recall code changes.

## Flow

```
question -> /recall -> matches? -> agent reads files -> answer
                    -> no match -> /find (research) -> answer
```

`/recall` = retrieval (cheap). `/find` = research + creation (expensive).

## LLM semantic grep

Agent spawns an Explore subagent that greps `summary:` across all
store dirs and judges relevance. The LLM is the search engine.

### Skill

```
ant/skills/recall-memories/SKILL.md
```

Protocol:

1. Spawn Explore subagent with query
2. Subagent greps `summary:` in `*.md` across all store dirs
3. Subagent reads each summary, judges relevance
4. Returns matches: file path, store name, why it matches

Scales to ~300 files per group; revisit if that becomes a limit.

## Progressive compression (episodes)

Session transcripts and diary entries compress into progressive
summaries. Both use the same file format and are indexed by `/recall`.

### Hierarchy

```
Episodes (from session transcripts):
  .claude/projects/<uuid>.jl  ─┐
  .claude/projects/<uuid>.jl  ─┤→ episodes/20260310.md  (day)
  .claude/projects/<uuid>.jl  ─┘      ↓
  episodes/20260310.md  ─┐
  episodes/20260311.md  ─┤→ episodes/2026-W11.md  (week)
  episodes/20260312.md  ─┘      ↓
  episodes/2026-W10.md  ─┐
  episodes/2026-W11.md  ─┤→ episodes/2026-03.md  (month)

Diary (from work log entries):
  diary/20260310.md  ─┐
  diary/20260311.md  ─┤→ diary/week/2026-W11.md      ↓  diary/month/2026-03.md
```

### File format

```markdown
---
summary: >
  - Shipped discord support
  - Resolved telegram auth token rotation
period: '2026-W11'
type: week
store: episodes
sources:
  - episodes/20260310.md
aggregated_at: '2026-03-17T02:00:00Z'
---

## Key decisions

...
```

### Compression schedule

`/compact-memories` skill, run via timed (cron), `context_mode: isolated`:

```
/compact-memories episodes day    → 0 2 * * *     daily
/compact-memories episodes week   → 0 3 * * 1     Monday
/compact-memories episodes month  → 0 4 1 * *     1st of month
/compact-memories diary week      → 0 3 * * 1     Monday
/compact-memories diary month     → 0 4 1 * *     1st of month
```

### Gateway injection

On session start, inject most recent of each type:

```xml
<episodes count="3">
  <entry key="20260314" type="day">summary</entry>
  <entry key="2026-W11" type="week">summary</entry>
  <entry key="2026-02" type="month">summary</entry>
</episodes>
```

Diary week/month summaries not injected — 14-day daily injection covers.
Week/month diary summaries exist for `/recall` searches over longer timeframes.

## recall-messages skill

`recall-messages` is a separate skill for searching chat message history
(the `messages` table), distinct from knowledge store recall. Message
history lookup is a direct DB query. Shipped alongside `recall-memories`.

## Not in scope

- Write operations (recall is read-only)
- Cross-group search

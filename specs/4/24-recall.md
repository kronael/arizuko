---
status: draft
---

# Recall — Knowledge Retrieval

**Status**: shipped (v1 only; v2 planned)

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
                    -> no match -> /facts (research) -> answer
```

`/recall` = retrieval (cheap). `/facts` = research + creation (expensive).

## v1: LLM semantic grep (current)

Agent spawns an Explore subagent that greps `summary:` across all
store dirs and judges relevance. The LLM is the search engine.

### Skill

```
container/skills/recall-memories/SKILL.md
```

Protocol:

1. Spawn Explore subagent with query
2. Subagent greps `summary:` in `*.md` across all store dirs
3. Subagent reads each summary, judges relevance
4. Returns matches: file path, store name, why it matches

### Scale

Up to ~300 files total: fast, one Explore call.
500+: too many summaries, switch to v2.

## v2: CLI retrieval + Explore judge (future)

Three steps:

```
1. Expand: agent generates ~10 search terms from the question
2. Retrieve: `recall "term"` for each (fast, CLI, mechanical)
3. Judge: spawn Explore with all results as context + question
```

### Hybrid search: FTS5 + vector

FTS5 catches exact keywords. Vector catches semantic similarity.
Together with LLM expansion (step 1) and LLM judgment (step 3),
covers all retrieval angles.

**sqlite-vec**: v0.1.6. Cosine distance. Vectors as raw float32 blobs.
**Embeddings**: Ollama `POST /api/embed`. `nomic-embed-text`, 768-dim.

### Config

`.recallrc` in group folder (TOML):

```toml
db_dir = ".local/recall"
embed_url = "http://10.0.5.1:11434/api/embed"
embed_model = "nomic-embed-text"

[[store]]
name = "facts"
dir = "facts"

[[store]]
name = "diary"
dir = "diary"
```

### DB

One DB per store in `.local/recall/` (derived cache, deletable):

```sql
CREATE TABLE entries (
  id INTEGER PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  path TEXT NOT NULL,
  summary TEXT,
  embedding BLOB,
  mtime INTEGER
);

CREATE VIRTUAL TABLE entries_fts USING fts5(
  key, summary,
  content='entries', content_rowid='id'
);

CREATE VIRTUAL TABLE entries_vec USING vec0(
  id INTEGER PRIMARY KEY,
  embedding float[768] distance_metric=cosine
);
```

### Lazy indexing

On each `recall` call: scan dirs, compare mtime, upsert new/changed,
prune deleted, then search. ~100ms/file for new entries (Ollama embed).

### Search

BM25 on FTS5 + cosine on sqlite-vec. RRF fusion (vector 0.7, BM25 0.3).

## Implementation notes

v2 recall tool lives in agent container (`container/agent-runner/`).
Language-agnostic concept — Go agent containers could implement the
same protocol with Go sqlite-vec bindings.

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
(the `messages` table), distinct from knowledge store recall. It does not
use the FTS5/sqlite-vec pipeline — message history lookup is a direct DB
query. Shipped alongside `recall-memories` in the kanipi skill sync.

## Not in scope

- Write operations (recall is read-only)
- Cross-group search
- Real-time indexing (lazy on query)

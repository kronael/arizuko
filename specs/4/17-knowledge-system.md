---
status: shipped
supersedes: [4/24-recall.md, 4/15-code-research.md]
---

# Knowledge System

The pattern underlying diary, facts, episodes, and user context.
Each is an instance of: markdown files in a directory, with
summaries selected and injected into agent context — or searched on
demand when the corpus is too large to inject.

## Memory Layers

| Layer    | Spec                   | Status  | Storage   |
| -------- | ---------------------- | ------- | --------- |
| Messages | 1/N-memory-messages.md | shipped | DB (SQL)  |
| Session  | 3/E-memory-session.md  | shipped | SDK (.jl) |
| Managed  | 1/M-memory-managed.md  | shipped | Files     |
| Diary    | 1/L-memory-diary.md    | shipped | Files     |
| User ctx | 3/7-user-context.md    | shipped | Files     |
| Facts    | 3/1-atlas.md           | shipped | Files     |
| Episodes | this spec              | shipped | Files     |

All memory layers shipped.

## The pattern

Given a directory of markdown files: **index** (scan, extract
`summary:` frontmatter), **select** (by recency, sender, relevance),
**inject** (into prompt context), **nudge** (prompt the agent to
write/update at defined moments).

## What fits this pattern

**Push layers** — small corpus, gateway injects automatically:

- **Diary** (`diary/*.md`) — date-keyed, 14 most recent, injected on
  session start via `diary.Read()` (`diary/diary.go`), called from
  `container/runner.go` prompt assembly. Agent writes via `/diary` skill.
- **User context** (`users/*.md`) — sender-keyed, gateway injects
  `<user>` pointer per message via `router.UserContextXml()`
  (`router/router.go`), agent reads file by default. Agent writes via
  `/users` skill.
- **Episodes** (`episodes/*.md`) — event-keyed, all or recent,
  inject on session start via `ReadRecentEpisodes()`
  (`container/episodes.go`). Progressive compression: sessions →
  episodes (day/week/month). Created via `/compact-memories`
  skill on cron schedule.

**Pull layers** — large corpus, agent searches on demand:

- **Facts** (`facts/*.md`) — topic-keyed, too many to inject all.
  Agent scans `summary:` frontmatter via grep, deliberates on relevance
  in `<think>`, reads matching files. The LLM's language understanding
  is the semantic matching — no embeddings needed.
  Researcher subagent writes; verifier reviews before merge
  (`ant/skills/find/SKILL.md` is canonical for the frontmatter schema
  and the two-phase research→verify protocol).

Push and pull are different. Push layers need gateway code (read files,
format XML, inject). Pull layers are agent-driven — the agent searches
and reads files directly using its native tools.

Messages, sessions, and MEMORY.md have their own implementations
and aren't forced into this pattern (see layer table above).

## Injection format

Push layers format selected summaries as XML, inserted into prompt:

```xml
<knowledge layer="diary" count="2">
  <entry key="20260306" age="today">summary text</entry>
  <entry key="20260305" age="yesterday">summary text</entry>
</knowledge>

<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />
```

Episodes use a sibling `<episodes count="N">` block with the same
`<entry>` shape plus a `type` attribute (`container/episodes.go`):

```xml
<episodes count="3">
  <entry key="20260314" type="day">summary</entry>
  <entry key="2026-W11" type="week">summary</entry>
  <entry key="2026-02" type="month">summary</entry>
</episodes>
```

Diary week/month summaries are **not** injected — the 14-day daily
injection already covers that window. They exist only so
`/recall-memories` can search longer timeframes.

## Nudges

Prompt the agent to write/update knowledge files:

- Hook-based: PreCompact, Stop, session start
- Message-based: first message from unknown user
- Skill-based: `/diary`, `/research`
- Scheduled: cron triggers researcher

Nudge text comes from skill config, not hardcoded in gateway.

## Push layer implementation

`diary.Read()`, `router.UserContextXml()`, and `ReadRecentEpisodes()`
each append to the prompt's `Annotations` slice at one injection point,
`container/runner.go`.

Each layer has its own formatter — no shared abstraction. Three similar
small formatters beats premature unification; they select on different
keys (date, sender, event) and only _look_ alike.

## Retrieval — `/recall-memories`

Generic search across knowledge stores. Read-only; never writes. All
stores use `summary:` frontmatter, so recall treats them uniformly. A
store is just a directory name:

```
facts/     diary/     users/     episodes/
```

Adding a store = one string. No code changes.

```
question -> /recall-memories -> matches? -> agent reads files -> answer
                             -> no match -> /find (research)   -> answer
```

### Separation from `/find`

- **`/recall-memories`** — retrieval only. Scan, match, return. Cheap.
- **`/find`** — research only. Create/refresh via subagents. Expensive.

### v1: LLM semantic grep (shipped)

Agent spawns an Explore subagent that greps `summary:` across all store
dirs and judges relevance. The LLM _is_ the search engine.

1. Grep `summary:` in `*.md` across all store dirs
2. Read each summary, judge relevance to the query
3. Return matches with file path, store name, and reasoning

After results, the agent deliberates in `<think>` (mandatory): what
does it say, does it answer, what gaps remain.

Skill: `ant/skills/recall-memories/SKILL.md` — always-present base
skill. Scales to ~300 files per group; beyond that, switch to v2.

### v2: CLI retrieval + Explore judge — designed, not built

The agreed shape for when a corpus outgrows the grep: expand the query
into ~10 terms, retrieve per term via a mechanical `recall` CLI (SQLite
FTS5 BM25 over a derived, safe-to-delete `.local/recall/` index), then
hand the pre-filtered candidates to the **same** Explore judge. Only the
candidate set changes.

The trigger is corpus size (~300 files), not answer quality. v1's
weakness is that grep reads every summary, costing tokens linearly —
a scale problem, not an accuracy one. Building v2 before hitting that
wall would trade a working simple thing for an index to keep coherent.

### recall-messages

`recall-messages` is a separate skill for searching chat history (the
`messages` table) — a direct DB query, not a file scan. Distinct from
knowledge-store recall; shipped alongside `recall-memories`.

### Knowledge-first: the relevance bar

Retrieval is only as good as the bar for "this fact answers the
question". The bar is deliberately brutal, and it is what lets the
system work without embeddings:

> A fact is relevant ONLY if it answers the question 100% correctly
> with trivial application. No interpretation, no inference, no
> "probably matches". Any doubt means the fact is NOT relevant.

The resulting decision tree, run in `<think>` before every answer:

- fact fully answers **and** is fresh → answer from it
- fact fully answers but is stale → `/find` to refresh, then answer
- no fact fully answers → `/find` to research, then answer

Paired with an evidence standard: cite `file:line` when referencing
code, quote when it clarifies, say so when evidence is missing, and
never fabricate a path, name, or line number. A loose bar plus a
generous model produces confident wrong answers from adjacent facts —
which is worse than a cache miss, because the miss is recoverable.

This is the contract a knowledge-backed agent's `SYSTEM.md` encodes;
product templates that ship one live under `ant/examples/<name>/`
([`../5/21-products.md`](../5/21-products.md)), not in this spec.
The codebase-Q&A instance of it is
[`../17/product-support.md`](../17/index.md).

### Decided (previously open)

- Recall scans `users/` — yes, it is in the stores list.
- Recall does **not** scan `MEMORY.md` — different format; the agent
  reads it directly.
- Episode format: same `<entry>` structure as diary, with `summary:`
  frontmatter.
- No embeddings in v1. The strict relevance rule compensates; add
  embeddings when the corpus demands it.
- `/find` has no hard timeout of its own — it is bounded by the
  container session timeout. Dedup across re-research is the agent's
  job (check existing facts first), not a platform guarantee.

## Progressive compression (episodes and diary)

Session transcripts and diary entries compress into progressive
summaries. Both use the same file format and are indexed by
`/recall-memories`.

Both roll up day → week → month. Episodes aggregate from session
transcripts (`.claude/projects/<uuid>.jl` → `episodes/YYYYMMDD.md` →
`episodes/YYYY-Www.md` → `episodes/YYYY-MM.md`); diary aggregates from
its daily entries into `diary/week/` and `diary/month/`.

Rollup files carry the same `summary:` frontmatter as any other store
entry, plus `period`, `type`, `store`, `sources`, and `aggregated_at`.
Keeping one frontmatter shape across every store is what lets
`/recall-memories` treat a month rollup and a fact file identically.

Compression runs as the `/compact-memories` skill on `timed` cron with
`context_mode: isolated` — daily for episode-days, Monday for weeks,
first-of-month for months. Isolated is required: a compaction turn that
inherited the group session would summarize its own summarizing.

## Not in scope

- Write operations in recall (read-only by design)
- Cross-group search
- Session state (container runner), message DB rows (store package),
  and `MEMORY.md` (Claude Code native) — different systems, not this
  pattern

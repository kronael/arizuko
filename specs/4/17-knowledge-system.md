---
status: shipped
---

# Knowledge System

The pattern underlying diary, facts, episodes, and user context.
Each is an instance of: markdown files in a directory, with
summaries selected and injected into agent context.

## Memory Layers

| Layer    | Spec                   | Status  | Storage   |
| -------- | ---------------------- | ------- | --------- |
| Messages | 1/N-memory-messages.md | shipped | DB (SQL)  |
| Session  | 3/E-memory-session.md  | shipped | SDK (.jl) |
| Managed  | 1/M-memory-managed.md  | shipped | Files     |
| Diary    | 1/L-memory-diary.md    | shipped | Files     |
| User ctx | 3/7-user-context.md    | shipped | Files     |
| Facts    | 3/1-atlas.md           | shipped | Files     |
| Episodes | 4/24-recall.md         | shipped | Files     |

All memory layers shipped.

## The pattern

Given a directory of markdown files:

1. **Index** — scan files, extract summaries (frontmatter or first N lines)
2. **Select** — choose which summaries to inject (by recency, sender, relevance)
3. **Inject** — insert selected summaries into agent prompt context
4. **Nudge** — at defined moments, prompt the agent to write/update files

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
  Researcher subagent writes; verifier reviews before merge.

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
`<entry>` shape (see `container/episodes.go`).

## Nudges

Prompt the agent to write/update knowledge files:

- Hook-based: PreCompact, Stop, session start
- Message-based: first message from unknown user
- Skill-based: `/diary`, `/research`
- Scheduled: cron triggers researcher

Nudge text comes from skill config, not hardcoded in gateway.

## Push layer implementation

**Shipped**: diary (`diary.Read()` in `diary/diary.go`), user context
(`router.UserContextXml()` in `router/router.go`), and episodes
(`ReadRecentEpisodes()` in `container/episodes.go`). The injection
point is `container/runner.go`, which appends each formatter's output
to the prompt's `Annotations` slice before joining.

Each layer has its own formatter — no shared abstraction. Three
similar small formatters beats premature unification.

## Pull layer: `/recall`

Agent-driven semantic search across knowledge stores. Read-only.
All stores use `summary:` frontmatter, so recall treats them
uniformly. A store is just a directory name.

### Stores

```
facts/     diary/     users/     episodes/
```

Each store = directory of `*.md` files with `summary:` in YAML
frontmatter. Adding a store = one string. No code changes.

### Separation from `/find`

- **`/recall`** — retrieval only. Scan, match, return. No writing.
- **`/find`** — research only. Create/refresh via subagents.

### v1: LLM semantic grep (shipped)

Agent spawns an Explore subagent that greps `summary:` across
all store dirs and judges relevance. The LLM is the search engine.

```
question -> /recall -> Explore subagent greps summary: fields
         -> judges relevance per file
         -> returns: path, store, why it matches
         -> agent reads matched files
         -> answers or escalates to /find if gaps remain
```

Explore protocol:

1. Grep `summary:` in `*.md` across all store dirs
2. Read each summary, judge relevance to query
3. Return matches with file path + reasoning

After results, agent deliberates in `<think>` (mandatory):
what does it say, does it answer, what gaps remain.

Scales to ~300 files. Beyond that, switch to v2.

See `specs/4/24-recall.md` for full recall spec.

### v2: CLI retrieval + Explore judge

Same Explore agent for judgment. Pre-filter via CLI tool
with FTS5 + vector search (hybrid BM25 + cosine).

Three steps:

1. **Expand** — agent generates ~10 search terms from query
2. **Retrieve** — `recall "term"` CLI for each (fast, mechanical)
3. **Judge** — Explore subagent with pre-filtered results

CLI tool: `container/agent-runner/recall` (Go binary).
Reads `.recallrc` from cwd. Uses SQLite FTS5 + sqlite-vec.

```
recall                   # sync index, show 5 newest
recall "telegram auth"   # search, show top 5
recall -10 "query"       # search, show top 10
```

DB per store in `.local/recall/` (derived cache, deletable).
Lazy indexing: scan dirs, compare mtime, embed new/changed.
Embeddings via Ollama `nomic-embed-text` (768-dim, ~100ms/file).

Search: FTS5 BM25 (keywords) + vector cosine (semantic).
RRF fusion: vector 0.7, BM25 0.3.

### Skill

```
ant/skills/recall-memories/SKILL.md
```

Always-present base skill. Teaches the agent the semantic
search protocol. `/recall-memories` scans facts/, diary/, users/,
episodes/. Read-only — never writes.

### Decided (previously open)

- `/recall` scans `users/` — yes, included in stores list
- `/recall` does NOT scan `MEMORY.md` — different format,
  agent reads it directly
- Episode format: same `<entry>` structure as diary, with
  `summary:` frontmatter
- Performance at 500+: switch to v2 CLI retrieval. No
  embeddings in v1, add when corpus demands it.

## What's left to build

1. **v2 CLI tool** — when corpus exceeds ~300 files (FTS5 + sqlite-vec)

## Relationship to existing specs

Layers built on this pattern:

- diary layer (shipped)
- user context layer (shipped)
- facts layer (shipped)
- episodes layer (shipped)

Different systems (not this pattern):

- Session state (container runner)
- Message DB rows (store package)
- MEMORY.md (Claude Code native)

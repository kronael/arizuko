## <!-- source: kanipi specs/3/D-knowledge-system.md, synced 2026-03-15 -->

## status: partial

# Knowledge System

The pattern underlying diary, facts, episodes, and user context.
Each is an instance of: markdown files in a directory, with
summaries selected and injected into agent context.

## Memory Layers

| Layer    | Spec                   | Status  | Storage   |
| -------- | ---------------------- | ------- | --------- |
| Messages | memory-messages.md     | shipped | DB (SQL)  |
| Session  | memory-session.md      | shipped | SDK (.jl) |
| Managed  | memory-managed.md      | shipped | Files     |
| Diary    | memory-diary.md        | shipped | Files     |
| User ctx | 3/7-user-context.md    | shipped | Files     |
| Facts    | 3/1-atlas.md           | shipped | Files     |
| Episodes | 4/B-memory-episodic.md | planned | Files     |

Diary, user context, and facts are shipped. Episodes not yet built.

## The pattern

Given a directory of markdown files:

1. **Index** — scan files, extract summaries (frontmatter or first N lines)
2. **Select** — choose which summaries to inject (by recency, sender, relevance)
3. **Inject** — insert selected summaries into agent prompt context
4. **Nudge** — at defined moments, prompt the agent to write/update files

## What fits this pattern

**Push layers** — small corpus, gateway injects automatically:

- **Diary** (`diary/*.md`) — date-keyed, 14 most recent, injected on
  session start via `formatDiaryXml()` in `index.ts` (shipped v0.7.0).
  Agent writes via `/diary` skill.
- **User context** (`users/*.md`) — sender-keyed, gateway injects
  `<user>` pointer per message, agent reads file by default (shipped).
  Agent writes via `/users` skill.
- **Episodes** (`episodes/*.md`) — event-keyed, all or recent,
  inject on session start. Not yet built.

**Pull layers** — large corpus, agent searches on demand:

- **Facts** (`facts/*.md`) — topic-keyed, too many to inject all.
  Agent scans `header:` frontmatter via grep, deliberates on relevance
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
<diary count="2">
  <entry key="20260306" age="today">summary text</entry>
  <entry key="20260305" age="yesterday">summary text</entry>
</diary>

<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />
```

## Nudges

Prompt the agent to write/update knowledge files:

- Hook-based: PreCompact, Stop, session start
- Message-based: first message from unknown user
- Skill-based: `/diary`, `/research`
- Scheduled: cron triggers researcher

Nudge text comes from skill config, not hardcoded in gateway.

## Push layer implementation

**Shipped**: diary (`formatDiaryXml()` in `diary.ts`) and user context
(`userContextXml()` in `router.ts`). Both ~5 lines each. Injection
point is `formatPrompt()` in `index.ts`.

**TODO**: Unified XML schemas. Each layer's format should be a typed
structure (DTO/schema) with a shared formatter:

```ts
// src/schemas/knowledge.ts — shared types
interface KnowledgeEntry {
  key: string;        // "20260306", "tg-123456", "2026-W09"
  attrs: Record<string, string>;  // age, confidence, etc.
  summary: string;
}

interface KnowledgeLayer {
  tag: string;        // "diary", "user", "episode"
  entries: KnowledgeEntry[];
}

function formatLayerXml(layer: KnowledgeLayer): string { ... }
```

Each layer defines its own schema and selection logic, calls the
shared formatter. Not a framework — just organized types and one
XML helper. The formatters stay in their own files:

- `diary.ts` → `formatDiaryXml()` uses `KnowledgeLayer` with tag `"diary"`
- `router.ts` → `userContextXml()` uses its own format (pointer, not entries)
- `episode.ts` (future) → `formatEpisodeXml()` with tag `"episode"`

Episodes would follow the same pattern once built.

## Pull layer: `/recall`

Agent-driven semantic grep — an Explore subagent reads summaries/headers
and judges relevance. The LLM IS the search engine. No embeddings, no
vector DB.

**Shipped** (as CLAUDE.md behavior): agent greps `facts/` `header:` fields,
deliberates in `<think>`, reads matches.

**TODO**: `/recall` skill — teaches the agent the semantic search protocol.
Always-present base skill (like `/diary`, `/users`).

### How it works

The agent spawns an **Explore subagent** with a query and target layers.
The Explore agent knows the protocol:

1. Scan `header:` (facts) or `summary:` (diary) YAML frontmatter
2. Read each candidate header, judge relevance to the query
3. Return matches with file paths + why each is relevant

The calling agent receives results, deliberates in `<think>` (mandatory
3-step: what does it say, does it answer, what gaps remain), then either
answers from the matched files or escalates to `/facts` for research.

```
Agent receives question
  → spawns Explore with "/recall: <question>"
  → Explore scans facts/ headers + diary/ summaries
  → Explore returns: "3 matches: facts/X.md (covers Y), diary/20260310.md (mentions Z)"
  → Agent reads matched files, deliberates in <think>
  → answers from knowledge, or runs /facts if gaps remain
```

### Layers scanned

| Layer    | Key field       | Example                              |
| -------- | --------------- | ------------------------------------ |
| Facts    | `header:` YAML  | dense paragraph, optimized for match |
| Diary    | `summary:` YAML | 5 bullet points per day              |
| Episodes | `summary:` YAML | week/month rollup (when built)       |

### Separation from `/facts`

- **`/recall`** — retrieval only. Scan, match, return. No writing.
- **`/facts`** — research only. Create/refresh facts via research +
  verification subagents. Called when `/recall` finds no matches.

Currently both are bundled in `/facts`. Separating them means the agent
can recall without the overhead of spawning researcher + verifier.

Scales to ~200 facts + 30 diary entries. At 500+ the header scan gets
expensive — embeddings or cached index would help but aren't needed yet.

## What's left to build

1. **Unified schemas** — typed DTOs for each layer's XML format,
   shared `formatLayerXml()` helper
2. **`/recall` skill** — always-present retrieval subagent across
   facts + diary (+ episodes when built)
3. **Separate recall from `/facts`** — `/facts` becomes research-only
4. **Episodes** — scheduled aggregation from diary, formatter, injection
5. **Episode aggregation prompt** — what to keep at each compression
   level (day→week→month)

## Open questions

- Should `/recall` also scan `users/` and `MEMORY.md`?
- Episode format: same `<entry>` structure as diary, or its own?
- Performance at 500+ facts: cached index vs embeddings vs status quo

## Relationship to existing specs

These specs describe specific layers built on this pattern:

- `specs/1/L-memory-diary.md` — diary layer (agent skills + gateway
  injection shipped)
- `specs/4/B-memory-episodic.md` — episodes layer (designed, not built)
- `specs/3/7-user-context.md` — user layer (shipped)
- `specs/3/3-code-research.md` — facts layer (shipped, see research + verify)

These specs describe different systems, NOT instances of this pattern:

- `specs/3/E-memory-session.md` — SDK state (container-runner.ts)
- `specs/1/N-memory-messages.md` — DB rows (router.ts SQL)
- `specs/1/M-memory-managed.md` — MEMORY.md (Claude Code native)

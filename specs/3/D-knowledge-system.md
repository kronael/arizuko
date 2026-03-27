---
status: draft
---

## <!-- trimmed 2026-03-15: layer status table removed, rich facts only -->

## status: partial

# Knowledge System

The pattern underlying diary, facts, episodes, and user context.

## The Pattern

Given a directory of markdown files:

1. **Index** -- scan files, extract summaries (frontmatter or first N lines)
2. **Select** -- choose which to inject (by recency, sender, relevance)
3. **Inject** -- insert selected summaries into agent prompt context
4. **Nudge** -- at defined moments, prompt agent to write/update files

## Push vs Pull

**Push layers** -- small corpus, gateway injects automatically:

- **Diary** (`diary/*.md`) -- date-keyed, 2 most recent, injected on
  session start. Agent writes via `/diary` skill. Shipped.
- **User context** (`users/*.md`) -- sender-keyed, match by message
  sender, inject on every message.
- **Episodes** (`episodes/*.md`) -- event-keyed, inject on session start.

**Pull layers** -- large corpus, agent searches on demand:

- **Facts** (`facts/*.md`) -- topic-keyed, too many to inject. Agent
  uses search tool (RAG/grep). Researcher subagent writes; verifier
  reviews before merge.

Push and pull are fundamentally different. Push needs gateway code
(read, format XML, inject). Pull needs a search tool and write process.
Don't unify them.

## Injection XML Format

```xml
<knowledge layer="diary" count="2">
  <entry key="20260306" age="today">summary text</entry>
  <entry key="20260305" age="yesterday">summary text</entry>
</knowledge>

<knowledge layer="user" count="1">
  <entry key="alice">Backend dev, works on validator-bonds</entry>
</knowledge>
```

## Nudges

- Hook-based: PreCompact, Stop, session start
- Message-based: first message from unknown user
- Skill-based: `/diary`, `/research`
- Scheduled: cron triggers researcher

Nudge text comes from skill config, not hardcoded in gateway.

## Open Questions

- Push layers: declarative (config) or imperative (code per layer)?
  Start with code; abstract only if third layer matches first two.
- Performance: 500 fact files? Cache index in memory, refresh on change.
- Researcher quality: auto-commit or require review? Unreviewed
  auto-injection is a misinformation pipeline.
- Can agent self-inject by reading files instead of gateway injection?
  (Injection is optimization for consistent context.)

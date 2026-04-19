---
status: partial
---

# Knowledge System

Pattern underlying diary, facts, episodes, user context.

## Pattern

Given a directory of markdown files:

1. **Index** — scan, extract summaries (frontmatter or first N lines).
2. **Select** — choose which to inject (recency, sender, relevance).
3. **Inject** — insert into agent prompt context.
4. **Nudge** — at defined moments, prompt agent to write/update.

## Push vs pull

Push (small corpus, gateway injects):

- **Diary** (`diary/*.md`) — date-keyed, 2 most recent on session start.
  Agent writes via `/diary`. Shipped.
- **User context** (`users/*.md`) — sender-keyed, match by message sender.
- **Episodes** (`episodes/*.md`) — event-keyed, session start.

Pull (large corpus, agent searches):

- **Facts** (`facts/*.md`) — topic-keyed. Agent uses search tool (grep/RAG).
  Researcher writes, verifier reviews.

Push needs gateway code (read, format XML, inject). Pull needs search +
write process. Don't unify.

## Injection XML

```xml
<knowledge layer="diary" count="2">
  <entry key="20260306" age="today">summary</entry>
  <entry key="20260305" age="yesterday">summary</entry>
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

Nudge text comes from skill config, not gateway.

## Open

- Push layers: declarative or imperative? Start imperative; abstract when
  a third layer matches the first two.
- 500 fact files? Cache index in memory, refresh on change.
- Researcher quality: auto-commit or review gate?
- Whether agent self-injection can replace gateway injection.

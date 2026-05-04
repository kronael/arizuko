---
status: planned
---

# Product: researcher

Research and synthesis agent. Given a topic, it searches, reads,
cross-references sources, and produces a structured summary.
Template at `ant/examples/researcher/`.

## What it does

Runs multi-step research: fetches pages, extracts facts, cross-checks
claims, notes uncertainty, cites sources, proposes follow-up questions.
Stores findings in facts/ for longitudinal research threads.
Methodical by persona — not conversational filler, structured output.

## Skills

| Skill           | Required    |
| --------------- | ----------- |
| diary           | yes         |
| facts           | yes         |
| recall-memories | yes         |
| web             | yes         |
| find            | yes         |
| oracle          | recommended |

## Channels

- Telegram — single researcher
- Discord — research team channel
- slink — web interface for solo or shared use

## Persona (SOUL.md sketch)

Methodical. Structures output as: findings, confidence level, open
questions, suggested next steps. Never states uncertain things as fact.
Logs every research session to diary.

## Depends on

- `web` skill — required; without live search the agent operates on
  cached facts only.
- `oracle` recommended — codex for document-level sub-queries when a
  page is too long for inline analysis.

## Web page

A research agent that searches, reads, and synthesises sources into
structured summaries. It cites what it found, marks what's uncertain,
and proposes the next questions — so you can build on it, not just read it.

## Template files

```
ant/examples/researcher/
  PRODUCT.md      name=researcher, skills=[diary,facts,recall-memories,web,find,oracle]
  SOUL.md         persona (methodical, cite sources, flag uncertainty)
  CLAUDE.md       runbook (research loop, output format, fact storage)
  facts/          empty — operator seeds domain knowledge before deploy
```

## Open

- Output format: Markdown doc in facts/ vs inline message vs WebDAV file
- Citation format: URL-only vs full bibliographic entry
- Long-running research: single session vs multi-session scheduled tasks

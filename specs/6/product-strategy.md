---
status: planned
brand: prometheus
---

# Product: strategy researcher (Prometheus)

Deep ongoing research on a domain — markets, competitors, trends.
Produces structured reports on a schedule and on demand.
Template at `ant/examples/strategy/`.

## Value prop

The agent is a permanent researcher embedded in your team's chat.
It tracks a defined domain continuously, synthesises what changes week
to week, and delivers a structured briefing — so decisions are made on
fresh intelligence, not stale intuition.

## What it does

- **Domain tracking**: monitors configured sources (news, filings,
  academic feeds, competitor sites) on a weekly schedule
- **Synthesis**: distills into a structured briefing: key developments,
  signals, implications, recommended watch items
- **On-demand deep-dives**: given a question ("what is X doing in Y
  market?"), runs oracle-powered multi-step research and delivers a memo
- **Longitudinal memory**: stores every briefing in facts/ so subsequent
  reports surface what changed, not just what is
- **Report delivery**: sends via send_file (PDF/markdown) and/or email

## Skills

| Skill           | Required | Notes                                         |
| --------------- | -------- | --------------------------------------------- |
| diary           | yes      |                                               |
| facts           | yes      | domain knowledge, prior briefings, watch list |
| recall-memories | yes      |                                               |
| web             | yes      | source monitoring, news, public filings       |
| oracle          | yes      | critical — deep document analysis, synthesis  |
| find            | yes      |                                               |

## Template folder

```
ant/examples/strategy/
  SOUL.md           — rigorous analyst; separates facts from inference;
                      always cites sources; marks confidence level;
                      proposes three implications per finding
  CLAUDE.md         — weekly report format: executive summary / key
                      developments / signals to watch / recommended actions;
                      store in facts/reports/YYYY-WW.md;
                      send_file to configured recipient on completion
  skills/           — diary, facts, recall-memories, web, oracle, find
  facts/
    domain.md       — seed: the domain, what matters, who the players are
    watchlist.md    — seed: specific sources, companies, topics to track
  tasks.toml        — weekly briefing cron (Monday 07:00)
```

## Channels

- Telegram or Discord — team channel for briefings and Q&A
- Email (emaid) — optional weekly report delivery
- slink — web access for distributed teams

## Depends on

- oracle (CODEX_API_KEY) — critical; without deep synthesis this is
  just a web-search wrapper
- davd — for report storage and editing
- timed — for weekly scheduled digest
- send_file MCP tool — for report delivery
- emaid _(optional)_ — email delivery of weekly briefing

## Developer capabilities embedded

Runs Python/bash scripts for data extraction, scraping, financial calcs
— standard oracle + bash grants, scoped per deployment.

## Web page pitch

A strategy researcher that never sleeps: monitors your domain, tracks
what changes week to week, and delivers a structured briefing to your
team on Monday morning. Ask it anything in between.

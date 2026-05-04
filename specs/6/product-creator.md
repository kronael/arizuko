---
status: planned
brand: inari
---

# Product: creator (Inari)

_Fox-god of the marketplace and the maker's hand. Knows that the work and the sell are the same thing._

Content creation pipeline and curator. Researches, drafts, refines,
and publishes long-form content — posts, newsletters, articles.
Operator approves before anything goes out.
Template at `ant/examples/creator/`.

## Value prop

The agent is the production layer: it monitors sources, identifies
what's worth writing about, drafts in the operator's voice, curates
drafts through revision rounds, and publishes when approved. The
operator steers; the agent does the legwork.

## What it does

- **Curation**: monitors configured sources (RSS, web, social feeds) on
  a schedule; surfaces what's worth covering with a brief pitch
- **Drafting**: given a brief or approved pitch, produces a full draft
  in the operator's voice; stores in davd workspace under `drafts/`
- **Revision**: operator replies with notes; agent revises in place
- **Publishing**: sends draft to bsky/mastodon/email on operator approval;
  until HITL ships, approval is an explicit `/publish <id>` command
- **Cross-posting**: routes the published piece to the socials product
  (or posts directly via configured adapters)

## Skills

| Skill           | Required | Notes                                          |
| --------------- | -------- | ---------------------------------------------- |
| diary           | yes      |                                                |
| facts           | yes      | brand voice, style guide, content rules        |
| recall-memories | yes      |                                                |
| web             | yes      | source monitoring, research for drafts         |
| oracle          | yes      | long document synthesis, multi-source drafting |
| find            | yes      |                                                |

## Template folder

```
ant/examples/creator/
  SOUL.md           — writes in operator's voice; never invents claims;
                      asks for brief before drafting; confirms platform
  CLAUDE.md         — curation loop: check sources, pitch → await approval;
                      draft loop: draft → revise → publish gate;
                      store all drafts in ~/drafts/<YYYYMMDD>-<slug>.md
  skills/           — diary, facts, recall-memories, web, oracle, find
  facts/
    voice.md        — seed with brand voice, style guide, do/don't list
    sources.md      — seed with RSS feeds, accounts, sites to monitor
  tasks.toml        — daily curation digest (schedule_task)
```

## Channels

- Telegram — single operator workflow (pitch → approve → draft → approve → publish)
- Discord — editorial team with reviewer roles via grants

## Depends on

- davd — critical; drafts live in the WebDAV workspace
- timed — for daily curation digest
- oracle (CODEX_API_KEY) — for multi-source synthesis
- bskyd or mastd — for publish
- HITL firewall _(deferred)_ — replaces manual `/publish` with automatic hold on publish calls
- emaid _(optional)_ — publish to newsletter / email list

## Developer capabilities embedded

Generates HTML newsletter templates, markdown-to-HTML conversion,
embed snippets for web pages — standard bash + oracle, no separate product.

## Web page pitch

A content pipeline that runs itself: monitors your sources, pitches
what's worth writing, drafts in your voice, and waits for your approval
before posting. You steer; it does the work.

---
status: planned
---

# Product: writer

Content authoring agent. Drafts posts, newsletters, and social content;
operator approves before publishing. Template at `ant/examples/writer/`.

See [5-authoring-product](5-authoring-product.md) for the earlier design;
this spec is the product-catalog entry that supersedes it once HITL ships.

## What it does

Given a brief or topic, drafts content in the operator's voice. Stores
drafts in the WebDAV workspace (`~/drafts/`). Operator reviews via
Telegram/Discord, approves or requests revision. On approval, publishes
to the configured platform (bsky or mastodon). Until HITL firewall ships,
the approval step is a manual "send /publish" command.

## Skills

| Skill           | Required                |
| --------------- | ----------------------- |
| diary           | yes                     |
| facts           | yes                     |
| draft           | yes                     |
| publish         | yes (gated — see below) |
| content-audit   | yes                     |
| web             | recommended             |
| oracle          | optional                |
| recall-memories | yes                     |

`publish` calls are held by the [HITL firewall](4-hitl-firewall.md)
(`hold: true` grant marker). Until HITL ships, the agent waits for an
explicit `/publish <draft-id>` command from the operator.

## Channels

- Telegram — single operator workflow
- Discord — editorial team (reviewer roles via grants)

## Persona (SOUL.md sketch)

Writes in the operator's voice; never invents brand claims. Asks for
brief before drafting. Confirms word count and platform before publishing.

## Depends on

- [HITL firewall](4-hitl-firewall.md) — for the automatic publish gate.
  Without it, the manual `/publish` command is the safety gate.
- A publishing adapter: `bskyd` or `mastd` must be running and the
  platform credentials must be in folder secrets.
- `davd` recommended — draft storage in WebDAV workspace.

## Web page

A content agent that drafts in your voice and waits for your approval
before posting. Connect it to Bluesky or Mastodon, review drafts in
Telegram or Discord, and publish with a single command.

## Template files

```
ant/examples/writer/
  PRODUCT.md      name=writer, skills=[diary,facts,recall-memories,draft,publish,content-audit,web]
  SOUL.md         persona (voice-match, approval-gated)
  CLAUDE.md       runbook (draft flow, approval gate, platform binding)
  facts/          operator seeds: brand voice, style guide, content rules
```

## Open

- Draft storage location: `~/drafts/` vs `pending_actions` row
- Multi-platform publish: single target per group vs configurable list
- Content-gap detection deferred (see [5-authoring-product](5-authoring-product.md))

---
status: deferred
---

# Authoring product

A product (per [R-products](R-products.md)) for content authoring: the
agent drafts posts and the operator approves before publishing. Ships at
`ant/examples/author/`.

Composition: persona (`SOUL.md` = author voice) + skills (`draft/`,
`publish/`, `content-audit/`) plus reused `research/`, `web/`.

## Publishing safety

The `publish` MCP tool is gated via the [HITL firewall](4-hitl-firewall.md).
The `publish` skill documents the hold so the agent expects
`{pending: true}` and reports the review queue link.

## Content target

Each authoring group has a `content_target` under
`/srv/data/.../web/pub/` for HTML rendering — drafts = preview,
published = permanent. `/pub/` is served by `vited`.

## Open

- Draft storage: `~/drafts/` vs shared `/drafts/` vs pinned in
  `pending_actions` — defer
- Platform binding: bsky vs mastodon first; group row vs config file vs
  new table — defer
- Content-gap detection (agent notices when operator hasn't published in
  N days) — defer

## Depends on

- [HITL firewall](4-hitl-firewall.md)
- An adapter exposing a publish capability (bsky or mastodon)
- [ant standalone](../5/b-ant-standalone.md) for the folder shape

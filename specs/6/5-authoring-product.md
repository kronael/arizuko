---
status: deferred
---

# Authoring product

A product (per `specs/4/products.md`): `template/products/author/`
with `SOUL.md` (voice), `HELLO.md` (greeting), `SYSTEM.md`, `CLAUDE.md`,
and skills `draft/`, `publish/`, `content-audit/` (plus reused
`research/`, `web/`). `arizuko group add <name> --product author`
copies the template into the group folder.

Publishing safety: `publish` MCP tool is gated via HITL firewall
([4-hitl-firewall.md](4-hitl-firewall.md)). The `publish` skill
documents the hold so the agent expects `{pending: true}`.

Each group has a `content_target` under `/srv/data/.../web/pub/` for
HTML rendering (drafts = preview, published = permanent). `/pub/`
served by vited.

Rationale: turn a stock agent into an opinionated author without a
bespoke daemon — pure configuration.

Unblockers: fixed catalog vs per-instance products; SOUL.md hot-reload
signal vs container restart; platform-account binding location (group
row vs config file vs new table); draft storage (`~/drafts/` vs shared
`/drafts/` vs always `pending_actions`); content-gap detection.
Depends on HITL firewall + at least one adapter exposing a publish
capability (bsky or mastodon first).

Out of scope v1: multi-author groups, auto-generated media,
cross-product composition, analytics loop.

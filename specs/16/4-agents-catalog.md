---
status: draft
depends: [V-web-vhosts, 36-yaml-manifests]
---

# Agents catalog

Multiplayer org-scale sharing implies discoverability: a person in the
org should be able to see _what agents live on this instance_ and how to
reach them, without operator access. arizuko has no such surface. dashd
lists groups, but behind `requireAdmin(**)` — operator-only. The
instance's running agents are invisible to the very teammates who are
meant to collaborate with them.

## What we steal

Two things, one shape. From **Centaur**: organizational agents are a
shared, advertised capability, not a private operator config. From the
**krons agents hub** (`https://krons.fiu.wtf/pub/krons/agents/`): a
_published, static, browsable catalog_ is a real and valuable artifact —
the hub proves an "agents reference page" is a thing people want to
read. arizuko already publishes an OpenAPI aggregator
(`/pub/arizuko/reference/openapi.html`, `5/36`); this is the same move
applied to the _agents_ of an instance instead of the _daemons_.

## arizuko-shaped design

A published, read-only catalog of an instance's folders, served from the
existing `/pub` static tree by vited (`5/V`) — no new daemon, no admin
auth, public by the same rule as any `/pub` page.

- A catalog entry per group that opts in: folder path, display name,
  one-line capability, reachable channels (which adapters route to it),
  and a slink/chat link if the group has a public web route
  (`route_tokens`, `5/W`).
- Source of truth is per-group config the operator already writes — the
  group's `CLAUDE.md`/`PERSONA.md` frontmatter (e.g. a `catalog:` block
  with `listed: true` + `summary:`). **Strict, not magical**: a group
  with no `catalog:` block is **unlisted** (private by default — an
  instance does not leak its full folder tree). Opt-in, never inferred.
- Generation rides the existing manifest/export path (`5/36`
  `arizuko export` / resreg) or a small vited render at request time
  over the listed groups — whichever the implementer picks; the catalog
  is a projection of existing rows + frontmatter, no new authoritative
  store.
- Visual identity is arizuko's `hub.css` (CLAUDE.md: arizuko visuals are
  load-bearing; we borrow the krons hub's _IA_, never its look).

## Out of scope

- Per-agent live status / health on the catalog (that's observability;
  the catalog is a directory, not a dashboard).
- Cross-instance / federated agent discovery — one instance's `/pub`.
- Any write surface — catalog is read-only; agent creation is `5/3`.
- Replacing dashd's operator group list — that stays admin-gated; this
  is the public, opt-in face.

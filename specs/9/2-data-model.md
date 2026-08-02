---
status: draft
depends: specs/5/17-openapi-mcp.md
---

# specs/9/2 — the cold / warm / hot tier model

## Why

The schema mixes three kinds of state in one bag, and the boundary is not
drawn anywhere explicit:

- **Cold — authoritative configuration.** ACL rules, routes, secret
  metadata, persona + skills references. Operator or agent writes it
  deliberately; it is the system's intent.
- **Warm — decision history.** Turn transcripts, grant changes, route
  mutations. Append-only, read for audit.
- **Hot — operational state.** Message queue, in-flight turns, cursors,
  sticky bindings, engagement TTLs, indexes. Rebuildable cache.

Without the line drawn, a table's columns drift across tiers: `chats`
carries a routing dimension (cold) next to `agent_cursor` and
`sticky_group` (hot), and nothing in the schema says which is which.

This vocabulary is load-bearing outside this spec. Root `CLAUDE.md` states
the rule that **every cold-tier management entity is a resreg resource**,
and `specs/CLAUDE.md` makes tier placement a definition-of-done item. Those
rules need this file to say what a tier is.

## The tiers

| Tier              | Examples                                                                                | Discipline                                         |
| ----------------- | --------------------------------------------------------------------------------------- | -------------------------------------------------- |
| Cold (config)     | ACL, routes, persona refs, skills selection, secret refs                                | A resreg resource — REST + MCP through one handler |
| Warm (decisions)  | turn transcripts, `audit_log`, grant + route mutations                                  | Append-only; written in the mutation's own tx      |
| Hot (operational) | message queue, cursors, in-flight turn state, engagement TTLs, sticky bindings, indexes | SQLite only, rebuildable; MCP-only agent actions   |

The tier decides the surface. Cold gets both faces (agent MCP + operator
REST) off one handler. Hot-tier agent actions (`reply`/`send`/`inspect_*`)
are the only hand-authored MCP-only tools — they have no operator twin
because there is nothing for an operator to manage.

## Entity notes worth keeping

**`secrets` — the blob never leaves SQLite.** Names and scopes are cold and
may be referenced anywhere; the AES-256-GCM ciphertext is not. A manifest
declares `slack_token = { scope = "folder", name = "slack" }` — the lookup
tuple, never the value. At spawn the resolver looks up `(scope, name)`,
decrypts in-process, and injects. See [`../8/E-encryption-at-rest.md`](../8/E-encryption-at-rest.md).

**`chats` is split across tiers and should not be.** The routing dimension
(which group a JID belongs to) is cold and duplicates what `routes` owns;
`agent_cursor` / `sticky_group` / `sticky_topic` are hot. Either move the
routing dimension into `routes` or introduce a join — but stop carrying both
tiers in one row.

**`messages` has no cold tier.** Every row is event-shaped: warm for the
content, hot for queue position and delivery state.

**`scheduled_tasks` splits cleanly.** Schedule, prompt and target folder are
cold; last-fired timestamp and error state are hot.

## Non-goals

Per-row tier columns. The tier is a property of the _column's meaning_, not
a value to store — encoding it in the schema would invite code to branch on
it. Documenting each entity's split in its `<pkg>/README.md` is the
mechanism.

## Open question

`audit_log` is per-daemon today, one table per owning DB. Whether it stays a
single append-only table per daemon or splits per resource is unresolved;
single is simpler and is what shipped.

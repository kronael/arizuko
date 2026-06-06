---
status: drafting
depends: specs/5/5-uniform-mcp-rest.md
---

# specs/8/2 — data model improvements (toward serialization-friendly entities)

## Why

Today's SQLite schema mixes three kinds of state in one bag:

- **Authoritative configuration** — ACL rules, routes, secret
  metadata, persona+skills references, products+deployments. These
  belong in git (Action 3); they need clean serialization shapes.
- **Decision history** — turn transcripts, grant changes, route
  mutations. These belong in git as commits with sidecar metadata.
- **Operational state** — message queue, in-flight turns, cursors,
  sticky-group bindings, engagement TTLs, indexes. These stay in
  SQLite; they are cache.

The boundary is not drawn explicitly in the schema today. Some
tables (`acl`, `routes`, `secrets`) carry authoritative data
intermixed with operational columns. Others (`chats`,
`scheduled_tasks`) blend per-row config with operational cursors.
Before any git move, the entities need a clean separation so each
row knows which tier it belongs to.

## What this spec is

A pre-migration spec. It does NOT define the on-disk git format
(that's `specs/8/3-git-as-truth.md`). It DOES decide which fields
in which tables migrate, which stay, and what shape they take.

## The three tiers (recap from `specs/8/index.md`)

| Tier              | Examples                                                                                | Lives in (post-phase)                |
| ----------------- | --------------------------------------------------------------------------------------- | ------------------------------------ |
| Cold (config)     | ACL, routes, persona refs, skills selection, secret refs, products, deployments         | Git — versioned, signed, distributed |
| Warm (decisions)  | turn transcripts, grant audit, route mutations                                          | Git via digest commit at turn end    |
| Hot (operational) | message queue, cursors, in-flight turn state, engagement TTLs, sticky bindings, indexes | SQLite only — rebuildable            |

## Concrete entity sharpening

For each entity, identify: (a) which columns are cold/warm/hot,
(b) what the serialized git shape looks like, (c) whether the
current schema needs to be split.

### `acl`, `acl_membership`

- Cold: rule string, scope, granted action, member list.
- Hot: nothing — pure config.
- Git shape: one file per acl rule (or one file per scope, with
  rules as TOML array). Lean per-scope file for human readability.

### `routes`

- Cold: match pattern, target folder, seq, observe-window settings.
- Hot: nothing — pure config.
- Git shape: one TOML file `routes.toml`, ordered by `seq`.

### `secrets`

**Secrets stay in SQLite. Git carries only names and scopes as
references.** The AES-256-GCM encrypted blob never leaves
`store/secrets.go`. This is non-negotiable: git is for distribution
and audit of _configuration_; secret blobs are operational state
that must stay on the operator's host, not in any artifact that
gets pushed/cloned/diffed.

- In SQLite (unchanged): name, scope (folder/user), AES-256-GCM
  ciphertext, salt, metadata (created, rotated, who).
- In git (reference only): a product or deployment in `agents.toml`
  declares `slack_token = { scope = "folder", name = "slack" }` —
  no value, no ciphertext, just the lookup tuple.
- At spawn: container's secret-resolver looks up `(scope, name)`
  in SQLite, decrypts in-process, injects into env. Existing
  primitives.

### `chats`

- Cold: route mapping (which group does this JID belong to). Could
  arguably move into `routes` table — `chats` becomes hot-only.
- Hot: `agent_cursor`, `sticky_group`, `sticky_topic`, `is_group`.
- Verdict: **split**. Move the routing dimension into `routes`;
  keep `chats` as the operational hot-path index.

### `messages`

- Cold: nothing — every message is event-shaped.
- Warm: the message itself (per-day digest commits).
- Hot: queue position, delivery status, retry state.
- Git shape: per-day per-chat JSONL files committed at digest time.
  Hot fields stay in SQLite.

### `scheduled_tasks`

- Cold: schedule, prompt, target folder.
- Hot: last-fired timestamp, error state.
- Split: cold to git as TOML in folder, hot stays.

### Products + deployments (new)

These don't exist yet as tables. They're introduced fully cold:
TOML files in git. SQLite gets a `deployments` operational cache
table for fast lookup. See `specs/8/3-git-as-truth.md` for the
serialization shape.

## Schema changes implied

Not all of these need to land in this spec — many can ride along
with Action 3. The minimal set this spec commits to:

1. Add `tier` column or use prefix conventions on table names to
   make the cold/warm/hot intent explicit (decide pattern; document).
2. Move routing dimension out of `chats` into `routes` (or
   introduce `chat_routes` join if direct move is too invasive).
3. Define the `deployments` table (cache shape only — the canonical
   data lives in `agents.toml` in git).
4. Define `audit_log` write contract — used for warm-tier
   decisions only (the per-turn sidecar). Cold tier does NOT
   emit audit rows because the working tree + git history IS the
   audit (per `specs/8/3-git-as-truth.md`).

Migrations under `store/migrations/` as usual. Schema owner is
`gated` (CLAUDE.md). Other daemons connect r/w but never migrate.

## Non-goals

- Defining the git on-disk format (`3-git-as-truth.md`).
- Implementing the dual-write writer (`3-git-as-truth.md`).
- Touching the message hot-path performance characteristics.

## Acceptance

- Each entity has an explicit cold/warm/hot column-level map in this
  spec (filled out during implementation).
- Migrations applied; old schemas still queryable for rollback.
- `make test -short` passes; integration tests covering the moved
  fields exist.
- Documentation in each `<pkg>/README.md` reflects the entity's
  new tier.

## Open questions

- ~~Secret blob location~~ — decided: stays in SQLite, git holds
  refs only. Closed.
- Routes ↔ chats refactor — clean move or compat shim?
- `audit_log` — append-only single table, or per-resource tables?
  Single table simpler; per-resource scales better. Lean single
  initially.

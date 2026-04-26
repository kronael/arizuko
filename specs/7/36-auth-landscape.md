---
status: shipped
---

> Reference spec — describes existing composition, not new work.
> Grounded in `auth/identity.go`, `grants/grants.go`, `store/routes.go`,
> `store/groups.go` (user_groups table). Written 2026-04-24 to fill
> the gap between `3/5-tool-authorization.md` (tool × tier matrix) and
> `7/28-acl.md` (user × folder ACL) — neither alone explains the whole.

# Auth Landscape — how `groups`, `user_groups`, `routes` compose into grants

Three tables and one folder path produce the grant rules an agent
container gets. None of them alone is enough; this spec names the
composition.

## The three layers

### `groups` table — folder hierarchy

Source of truth for what exists and tier.

- `folder TEXT PRIMARY KEY` — e.g. `atlas/support`
- Tier derived: `min(strings.Count(folder, "/"), 3)` — see
  [`auth/identity.go:19`](../../auth/identity.go)
- `world` = first path segment, same helper

**Path is identity, depth determines default grants.** Tier 0 is the
**root** folder. Tier 1 is a **world** (top-level tenant). Depth-2 and
depth-3+ are sub-groups; suggested labels (`org / branch / unit /
thread`) are advisory — see `ant/CLAUDE.md` for the depth → label
cross-walk. The system reads paths, not labels.

### `user_groups` table — who can act on a folder

Glob ACL. One row per (user-sub, glob) grant.

- `(user_sub TEXT, glob TEXT, granted_at TEXT)` — see
  [`store/migrations/0006-user-groups.sql`](../../store/migrations)
- `glob` is a folder pattern: `**` (operator), `atlas`, `atlas/**`, `atlas/support`
- Who is "operator" is emergent: any user with a `**` row. No tier-0
  sentinel; see [`7/28-acl.md`](28-acl.md) for the full ACL model.

### `routes` table — what JIDs route where

What inbound platform JIDs land in which folder.

- `(id, seq, match TEXT, target TEXT, impulse_config)` — flat table
- `match` is space-separated glob pairs: `platform=telegram room=-123`
- `target` is the folder that receives messages matching `match`
- See [`1/F-group-routing.md`](../1/F-group-routing.md) for the
  match language

## The composition

A container spawn for folder `F` produces its grant rules in three
steps:

```
1. Identity     = auth.Resolve(F)
                  → {Folder: F, Tier: min(depth, 3), World: firstSeg}

2. RoutedJIDs   = store.RouteSourceJIDsInWorld(world)
                  → JIDs that route to anything inside the world

3. Rules        = grants.DeriveRules(store, F, Tier, World)
                  → tier-based list, extended by platformRules(RoutedJIDs)
```

Final rules list is what the MCP server uses to gate every tool call.
See [`grants/grants.go:151-178`](../../grants/grants.go).

### Where each table is consulted

| Gate                                                                    | Reads from                                  | Produces                                          |
| ----------------------------------------------------------------------- | ------------------------------------------- | ------------------------------------------------- |
| who can send a message from outside the container to this folder at all | `user_groups` glob vs folder                | yes/no at API surface                             |
| what tools does the agent in folder F have                              | `groups` (tier from folder depth)           | tier slot                                         |
| which platforms does that folder's tools scope to                       | `routes` where `target` matches folder      | platform-scoped rules like `send(jid=telegram:*)` |
| does a specific tool call pass                                          | `grants.CheckAction(rules, action, params)` | yes/no at MCP call                                |

### Example

User `u:github-1234` sends a Telegram message to chat `-123`, which is
routed to folder `atlas/support`:

1. **Inbound admission** — `u:github-1234` must have a `user_groups`
   row whose glob matches `atlas/support`. Could be `**` (operator),
   `atlas/**` (world owner), `atlas/support` (direct grant). If none
   → 403, onbod admission flow if `ONBOARDING_ENABLED`.
2. **Routing** — `routes` table resolves `telegram:-123` → target
   `atlas/support`.
3. **Agent spawn** — container starts with folder `atlas/support`.
4. **Tier** — `auth.Resolve("atlas/support")` → tier 1.
5. **RoutedJIDs** — `RouteSourceJIDsInWorld("atlas")` returns all
   `telegram:*`, `discord:*`, etc. JIDs routed to any atlas subgroup.
6. **Rules** — `grants.DeriveRules` produces:
   - Tier 1 base: `send`, `reply`, `send_file`
   - Per-platform: `send(jid=telegram:*)`, `post(jid=telegram:*)`, ...
   - Tier-1 fixed: `schedule_task`, `register_group`, `delegate_group`, ...
   - `share_mount(readonly=false)`
7. **MCP** — IPC server filters registration by these rules. Calls
   are gated at registration (tool appears or not) AND at runtime
   (`grants.CheckAction` per-invocation).

## Key invariants

- **Tier ≠ ACL.** `user_groups` (who) is orthogonal to tier (what).
  A user with a `**` grant still sees only the tools their folder's
  tier allows; a tier-0 root folder with no human user_groups rows
  is still an operator-controlled surface (CLI-only).
- **No silent inheritance.** Each folder derives its own rules from
  its own routes. Children don't inherit parent's JIDs.
- **Route presence gates platform access.** No route for
  `bluesky:*` to folder F → no `send(jid=bluesky:*)` rule →
  agent can't post to Bluesky from F even if an adapter is running.

## Specs this unifies

- [`3/5-tool-authorization.md`](../3/5-tool-authorization.md) — the tier × action matrix
- [`7/28-acl.md`](28-acl.md) — the `user_groups` glob model
- [`1/F-group-routing.md`](../1/F-group-routing.md) — the `routes` match language
- [`3/V-platform-permissions.md`](../3/V-platform-permissions.md) — deferred; its
  concern is now addressed by `RoutedJIDs + platformRules` composition
  rather than a separate `platform_grants` table

## Open items

- Subgroup inheritance: intentionally absent today. If a spec wants
  "worker inherits world's platform" without per-route editing,
  that's a new proposal, not a fix.
- Read-action scoping: `fetch_history`, `inspect_*` aren't
  per-platform-gated today — only tier-gated. If per-platform read
  control matters, small addition — not in this spec's scope.

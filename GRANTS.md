---
status: shipped
---

> Reference doc — describes existing composition, not new work.
> Grounded in `auth/identity.go`, `auth/acl.go`, `auth/policy.go`,
> `auth/middleware.go`, `grants/grants.go`, `store/auth.go`,
> `store/groups.go`, `store/routes.go`, `store/grants.go`,
> `store/secrets.go`, `gateway/gateway.go`, `container/runner.go`.
> Last verified 2026-05-01. Originally written 2026-04-24 to fill the
> gap between `specs/3/5-tool-authorization.md` (tier × action matrix)
> and `specs/5/29-acl.md` (user × folder ACL) — neither alone explains
> the whole.

# Grants — how folders, ACL rows, routes and secrets compose into what an agent can do

Three SQLite tables, one folder path, and a fistful of env vars produce
the rules an agent container is spawned with. Maintainer reference:
read it with the codebase open. Lives at root (not under `specs/`)
because it documents the contract between today's daemons, not a
future design.

How to read this: see `SECURITY.md` for boundaries and threat model;
see `ARCHITECTURE.md` for the daemon and package graph.

## The four layers

### `groups` table — folder hierarchy

Source of truth for what exists. Defined in
`store/migrations/0020-groups-refactor.sql` (later patched by 0041,
which dropped `state` / `spawn_ttl_days` / `archive_closed_days` —
groups exist until explicitly removed; and 0044, which added
`product`); current columns are `folder TEXT PRIMARY KEY`, plus
`name`, `added_at`, `container_config`, `slink_token`, `parent`,
`updated_at`, `product`. Read via `store.Store.GroupByFolder` /
`AllGroups`.

Tier and world are derived from the folder string, not stored:

- `auth.Resolve(folder)` returns `Identity{Folder, Tier, World}`
- Tier = `min(strings.Count(folder, "/"), 3)` — root=0, world=1, depth-2=2, depth-3+=3
- World = first path segment

Path is identity, depth determines default grants. Suggested labels
(`org / branch / unit / thread`) are advisory — the system reads paths,
not labels.

### `user_groups` table — who can act on a folder

Glob ACL keyed by user_sub. Defined in
`store/migrations/0013-user-groups.sql` (`granted_at` column added in
0026):

```sql
CREATE TABLE user_groups (
    user_sub   TEXT NOT NULL,
    folder     TEXT NOT NULL,    -- glob pattern despite the column name
    granted_at TEXT,              -- nullable; pre-0026 rows have none
    PRIMARY KEY (user_sub, folder)
);
```

Patterns: `**` (operator), `*` (any single segment), `pub/*`, `atlas/**`,
`atlas/support`. Match logic in `auth.MatchGroups`
(`auth/acl.go`) — segment-wise, `*` does not cross `/`, `**` matches
zero or more segments. No rows = no access. Operator is implicit: a
user with a `**` row simply matches every folder.

Reads: `store.Store.UserGroups(sub) []string` returns the user's
patterns; `MatchGroups(patterns, folder) bool` answers "may sub touch
folder?". Writes: `Grant`, `Ungrant`, `Grants`.

### `routes` table — what JIDs route where

Inbound match-target table. Current shape from
`store/migrations/0022-routes-match.sql`:

```sql
CREATE TABLE routes (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  seq            INTEGER NOT NULL DEFAULT 0,
  match          TEXT    NOT NULL DEFAULT '',
  target         TEXT    NOT NULL,
  impulse_config TEXT
);
```

`match` is a space-separated list of `key=glob` pairs evaluated by
`router.RouteMatches` (e.g. `platform=telegram room=-123`,
`room=foo verb=command`). `target` is the folder receiving matched
messages. Routes are evaluated in `(seq, id)` order; first match wins.
See `specs/1/F-group-routing.md` for the match language.

`store.Store.RouteSourceJIDsInWorld(scope)` reconstructs the distinct
`platform:room` JIDs whose target folder is `scope` or a descendant —
it is what `grants.DeriveRules` uses to derive per-platform rules.

Local addressing: there is no `local:` prefix any more. Bare folder
paths without `:` route via `gateway.LocalChannel.Owns`
(`gateway/local_channel.go`) — real channel JIDs always carry a
`platform:` prefix, so the absence of `:` is sufficient. Used by
escalation/delegation and by `arizuko send`.

### `grant_rules` table — per-folder rule overlay

Optional folder-keyed extension to the tier defaults. Read by
`store.Store.GetGrants(folder) []string`, written by `SetGrants`,
managed via the `get_grants` / `set_grants` MCP tools (tier 0–1 only,
gated through `auth.Authorize`). Appended to the rules list that
`grants.DeriveRules` produces — see `gateway/gateway.go` (search for
`DeriveRules` in `runAgentWithOpts`).

## The composition

A container spawn for folder `F` with chat JID `J` produces its
rule list and env in three steps. The canonical site is
`Gateway.runAgentWithOpts` in `gateway/gateway.go`:

```
1. Identity     = auth.Resolve(F)
                  → {Folder: F, Tier: min(depth, 3), World: firstSeg}

2. Rules        = grants.DeriveRules(store, F, id.Tier, id.World)
                  ++ store.GetGrants(F)
                  → tier-default list, extended by routed-platform
                    rules and by the per-folder grant_rules row

3. Env          = container.resolveSpawnEnv(store, base, F, J)
                  → base ∪ FolderSecretsResolved(F)
                    ∪ UserSecrets(UserSubByJID(J))   (single-user chats only)
```

The rule list is injected into `start.json` for the agent and used
by the in-container MCP server to filter the tool manifest and gate
each call (`grants.MatchingRules` at registration,
`grants.CheckAction` at invocation). The env is exported into the
agent process.

### Tier defaults — `grants.DeriveRules`

From `grants/grants.go`:

| Tier | Default rules                                                                                                                                                             |
| ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | `["*"]` — instance root, anything                                                                                                                                         |
| 1    | `basicSendActions` ++ `platformRules(RouteSourceJIDsInWorld(world))` ++ `tier1FixedActions` ++ `share_mount(readonly=false)`                                              |
| 2    | `basicSendActions` ++ `platformRules(RouteSourceJIDsInWorld(folder))` ++ `share_mount(readonly=true)` — narrower platform scope: only JIDs routed to F or its descendants |
| 3+   | `["reply", "send_file", "like", "edit"]`                                                                                                                                  |

`basicSendActions = {send, send_file, reply}`.

`platformActions = {send, send_file, reply, forward, post, quote,
repost, like, dislike, delete, edit}` — for each platform appearing in
the routes scoped to the caller's tier (tier 1: the world's routes;
tier 2: the folder subtree only — see `RouteSourceJIDsInWorld` calls
above), every action is added as `<action>(jid=<platform>:*)`.

`tier1FixedActions = {schedule_task, register_group, escalate_group,
delegate_group, get_routes, set_routes, add_route, delete_route,
list_tasks, pause_task, resume_task, cancel_task}`.

### Where each table is consulted

| Gate                                             | Reads from                                                                                 | Produces                                             |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------ | ---------------------------------------------------- |
| who can reach a folder over HTTP at all          | proxyd-signed `X-User-*` headers verified by `auth.RequireSigned` / `StripUnsigned`        | identity headers stripped on failure → 303 to /login |
| does this user have any claim on folder F        | `user_groups` patterns vs F via `auth.MatchGroups`                                         | yes/no at API surface (e.g. proxyd dav)              |
| what default tools does folder F's agent see     | `groups` (tier from depth) → `grants.DeriveRules`                                          | tier-default rule slot                               |
| which platforms scope to folder F's outbound     | `routes` where `target` is F or a descendant (tier 2) / world descendant (tier 1)          | `<verb>(jid=<platform>:*)` rules                     |
| any folder-specific rule overlays                | `grant_rules.rules` for F                                                                  | extra rules appended after tier defaults             |
| does a specific tool call pass                   | `grants.CheckAction(rules, action, params)` in the in-container MCP server                 | yes/no per invocation                                |
| is the addressed JID inside the agent's subtree  | `auth.Authorize` (subtree containment via `routes`-resolved owning folder)                 | yes/no per outbound verb                             |
| is the channel/group muted at this instance      | env-only: `SEND_DISABLED_CHANNELS`, `SEND_DISABLED_GROUPS` (`gateway.canSendTo*`)          | outbound recorded but platform send skipped          |
| what env / secrets does the container start with | `secrets` table (folder ancestors + optional user overlay) via `container.resolveSpawnEnv` | merged plaintext env, deepest-wins                   |

### Worked example

User `u:github-1234` sends a Telegram message to chat `-123`, which is
routed to folder `atlas/support`:

1. **Inbound admission** — proxyd verifies the user's session and
   stamps `X-User-Sub`/`X-User-Sig`. Backend mounts using
   `auth.RequireSigned` reject mismatched sigs. For folder access
   checks (e.g. dav), `auth.MatchGroups(store.UserGroups(sub), folder)`
   must allow `atlas/support`. Could be `**` (operator), `atlas/**`
   (world owner), or `atlas/support` (direct grant). If the user has no
   matching row, onbod's admission queue gates them when
   `ONBOARDING_ENABLED`.
2. **Routing** — `routes` table resolves a row with
   `match='platform=telegram room=-123'`, target `atlas/support`.
3. **Agent spawn** — gateway starts a container for folder
   `atlas/support`.
4. **Tier** — `auth.Resolve("atlas/support")` → tier 1 (depth 1, sole `/`).
5. **Routed JIDs in world** — `RouteSourceJIDsInWorld("atlas")` collects
   every `<platform>:<room>` whose target sits under `atlas/`.
6. **Rules** — `grants.DeriveRules(store, "atlas/support", 1, "atlas")`
   yields:
   - `send`, `reply`, `send_file`
   - per-platform: `send(jid=telegram:*)`, `post(jid=telegram:*)`, …
   - `schedule_task`, `register_group`, `escalate_group`,
     `delegate_group`, `get_routes`, `set_routes`, `add_route`,
     `delete_route`, `list_tasks`, `pause_task`, `resume_task`,
     `cancel_task`
   - `share_mount(readonly=false)`
   - …followed by anything in `grant_rules` for `atlas/support`.
7. **Env** — `resolveSpawnEnv` merges `FolderSecretsResolved("atlas/support")`
   over the base env (catch-all `root` < `atlas` < `atlas/support`,
   deepest wins). Per-user secrets do not enter the container — the
   broker resolves them at tool-call time inside `gated` (spec 9/11).
   Stored as plaintext in `secrets` (encryption at rest deferred).
8. **MCP** — IPC server filters tool registration by these rules
   (`grants.MatchingRules`) AND gates each call (`grants.CheckAction`).
   Outbound verbs additionally pass through `auth.Authorize`, which
   enforces subtree containment: the targeted JID's owning folder
   (resolved via `store.DefaultFolderForJID`) must equal `id.Folder`
   or be a descendant of it.

## Grants as the tool pre-filter

Other agent systems narrow the advertised tool set with a **tool
pre-filter**: a cheap-model classifier picks ≤K relevant tools per
turn before the main model sees the list (e.g. AnythingLLM's
"Intelligent Skill Selection"; muaddib's per-mode reducer plays a
similar token-budget role for context). arizuko reaches the same
intent — don't waste model context on tools the caller can't use —
via grants, at a different evaluation point:

- **When**: spawn-time, once per container run, not per-turn.
  `gateway.runAgentWithOpts` composes the rule list from
  `grants.DeriveRules` (tier defaults + routed-platform rules) plus the
  `grant_rules` overlay (`store.GetGrants`); `user_groups` containment
  is enforced separately at call time via `auth.Authorize`.
- **What**: the rule list is the filter. At MCP registration
  (`ipc/ipc.go` `buildMCPServer`) a tool with no matching rule is
  dropped from `tools/list` entirely; a tool with any matching rule
  (including param-scoped) stays visible, and `grants.CheckAction`
  rejects out-of-scope invocations at call time.
- **Why static**: tier + ACL + route presence already answer "what
  could this folder ever call." A per-turn classifier would re-decide
  the same question every turn against the same inputs — pure overhead.

The shape difference is the trade: static pre-filtering misses
"this turn doesn't need bluesky tools even though the folder has
them"; dynamic pre-filtering pays a classifier round-trip and risks
hiding a tool the agent actually wanted. arizuko's bet is that an
agent's effective tool set is a property of the folder, not the
sentence.

## Key invariants

- **Tier ≠ ACL.** `user_groups` (who) is orthogonal to tier (what).
  A user with a `**` grant still sees only the tools their folder's
  tier allows; a tier-0 root folder with no human `user_groups` rows
  is still an operator-controlled surface (CLI-only).
- **No silent inheritance of routes.** Each folder derives its own
  rules. Tier-1 worlds see all platforms routed anywhere under
  themselves; tier-2+ groups see only platforms routed at their own
  subtree.
- **Route presence gates platform access.** No route for `bluesky:*`
  to anything under folder F's scope → no `send(jid=bluesky:*)` rule
  → agent can't post to Bluesky from F even if an adapter is running.
- **Outbound subtree containment is independent of grants.** Even a
  tier-0 root cannot direct-send cross-world via outbound verbs;
  cross-world traffic goes through `escalate_group` / `delegate_group`,
  each with its own `auth.Authorize` rule.
- **Secrets ride the same hierarchy as grants but resolve
  independently.** Folder ancestors walk root→F deepest-wins; per-user
  secrets resolve inside the broker at tool-call time and never enter
  container env. v1 stores plaintext per spec 9/11 (encryption at rest
  deferred).

## Code pointers

- Identity & ACL: `auth/identity.go` (`Resolve`, `WorldOf`),
  `auth/acl.go` (`MatchGroups`), `auth/policy.go` (`Authorize`)
- Identity transport: `auth/middleware.go` (`RequireSigned`, `StripUnsigned`)
- Tier-default grants engine: `grants/grants.go` (`DeriveRules`,
  `CheckAction`, `MatchingRules`, `ParseRule`)
- Stores: `store/auth.go` (`UserGroups`, `Grant`, `Ungrant`),
  `store/grants.go` (`GetGrants`, `SetGrants`),
  `store/groups.go` (`RouteSourceJIDsInWorld`, `DefaultFolderForJID`),
  `store/routes.go` (route CRUD),
  `store/secrets.go` (`FolderSecretsResolved`, `UserSecrets`)
- Spawn-time composition: `gateway/gateway.go` (`runAgentWithOpts`),
  `container/runner.go` (`resolveSpawnEnv`)
- In-container gating: `ipc/ipc.go` (`buildMCPServer`)

## Specs this unifies

- `specs/3/5-tool-authorization.md` — tier × action matrix
- `specs/5/29-acl.md` — `user_groups` glob model
- `specs/1/F-group-routing.md` — `routes` match language
- `specs/3/V-platform-permissions.md` — deferred; concern subsumed
  by `RouteSourceJIDsInWorld + platformRules` composition rather than
  a separate `platform_grants` table

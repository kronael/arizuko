---
status: spec
depends: [5-uniform-mcp-rest, 6-middleware-pipeline]
supersedes-in-part: [5/A-auth-consolidated.md, 4/19-action-grants.md]
---

# Unified ACL — one primitive, three principals

Authorization in arizuko is one row, one question:

```
(principal, action, scope, params, predicate, effect) → allow | deny
```

Every actor — human, agent, channel-room, role — is a principal of
the same shape. Every decision goes through one `Authorize` call. Two
tables: `acl` for grants, `acl_membership` for identity-indirection
(role membership, JID claims, role hierarchy — same graph).

## Essence

One row asks one question:

```
(principal, action, scope, params, predicate, effect) → allow | deny
```

- **principal** — who is asking. Globbed. Namespaces below.
- **action** — `interact`, `admin`, `mcp:<tool>`, `*`. Implication
  lattice below.
- **scope** — folder path or glob. Same grammar as today
  (`auth/acl.go:21`).
- **params** — optional name=glob predicates over call args
  (`jid=telegram:*`). Carries forward `grants/grants.go:104`.
- **predicate** — optional claim condition (`discord:guild=G123`,
  `github:org=acme`). Empty = no claim required.
- **effect** — `allow` or `deny`. Deny wins.

Tier defaults stay in code (`grants.DeriveRules`). Only operator
overrides become rows.

## Schema

Two tables. Permissions and identity-indirection stay separated —
different concerns, different indexes, different write paths.

```sql
-- Permissions: who can do what, where, with what effect.
CREATE TABLE acl (
  principal   TEXT NOT NULL,
  action      TEXT NOT NULL,
  scope       TEXT NOT NULL,
  effect      TEXT NOT NULL DEFAULT 'allow',  -- 'allow' | 'deny'
  params      TEXT NOT NULL DEFAULT '',       -- 'jid=telegram:*'
  predicate   TEXT NOT NULL DEFAULT '',       -- 'discord:guild=X'
  granted_by  TEXT,
  granted_at  TEXT NOT NULL,
  PRIMARY KEY (principal, action, scope, params, predicate, effect)
);
CREATE INDEX acl_by_principal_action ON acl(principal, action);
CREATE INDEX acl_by_scope             ON acl(scope);

-- Identity / role indirection: child principal IS-A parent principal.
-- One graph subsumes JID claims, role membership, and role hierarchy.
CREATE TABLE acl_membership (
  child       TEXT NOT NULL,
  parent      TEXT NOT NULL,
  added_by    TEXT,
  added_at    TEXT NOT NULL,
  PRIMARY KEY (child, parent)
);
CREATE INDEX acl_membership_by_child ON acl_membership(child);
```

Row-count estimate: ~100 user grants + ~30 operator overrides + ~20
room-acl rows + ~10 role rows. SQLite handles 10⁵ rows trivially.

## Principal namespace

Canonical formats, globbed segment-wise (no `/`-crossing for `*`):

| Namespace          | Example                  | Meaning                                              |
| ------------------ | ------------------------ | ---------------------------------------------------- |
| OAuth sub          | `google:114019...`       | Canonical human sub from `auth/oauth.go`.            |
| Folder agent       | `folder:atlas/eng`       | Agent container spawned at this folder.              |
| Platform identity  | `telegram:user/123456`   | Channel-side identity, no OAuth yet.                 |
| Room identity      | `discord:837.../1504...` | Channel/room JID — the route audience.               |
| Role               | `role:operator`          | Indirection principal. Members via `acl_membership`. |
| Operator wildcard  | `**`                     | Any principal.                                       |
| Namespace wildcard | `google:*`, `folder:**`  | Any sub in namespace.                                |

Glob anchoring is segment-wise on `/` AND on `:` (so `google:*` does
not match `google:114/sub`). OAuth subs contain no `/`; the invariant
is safe.

## Action namespace

Three families, one implication lattice:

```
*       ⊃   admin   ⊃   interact
*       ⊃   mcp:<tool>
admin   ⊃   mcp:<tool>
```

- `interact` — read + send in scope. The floor for any participant.
- `admin` — write platform state at scope (routes, grants, secrets,
  membership).
- `mcp:<tool>` — one MCP tool. The agent's call surface. `mcp:*`
  matches every tool.
- `*` — every action.

Implication is evaluated by `Authorize`, not denormalized. Granting
`admin` is **not** equivalent to inserting one row per `mcp:<tool>`.
The lattice is the contract.

## Membership: roles, JID claims, channels

All three are `acl_membership` edges — same primitive, three uses.

| Edge                                                   | What it expresses |
| ------------------------------------------------------ | ----------------- |
| `acl_membership(google:114alice, role:operator)`       | alice is operator |
| `acl_membership(discord:user/811..., google:114alice)` | JID claim         |
| `acl_membership(role:senior, role:operator)`           | role hierarchy    |

**Implicit principal set at message arrival.** When discd receives a
message from `discord:user/811...` in room `discord:837.../1504...`,
the gateway expands the caller's principal set to include BOTH:

```
expand(caller_jid, room_jid) =
    {caller_jid, room_jid}
  ∪ {p : transitively via acl_membership from caller_jid}
  ∪ {p : transitively via acl_membership from room_jid}
```

The room JID carries the route's baseline grants; transitive
membership carries personal grants. Both flow through one `acl`
lookup.

Postgres / IAM mapping:

| Real-world             | Mirrored as                  |
| ---------------------- | ---------------------------- |
| `GRANT role TO user`   | `acl_membership(user, role)` |
| `GRANT <perm> TO role` | `acl(role, action, scope)`   |
| AWS IAM group          | `role:<name>` is the group   |
| Google IAM binding     | The `acl` row IS the binding |

**Predefined roles** seeded at `arizuko create`:

- `role:operator` — has `(role:operator, *, **, allow)`. First OAuth
  sub bound via `acl_membership` at create time.
- `role:org-admin`, `role:org-member` — empty templates operators wire
  up via `acl.grant` / `membership.add` MCP tools.

**Cycle prevention** on `acl_membership` writes: transactional
recursive walk from the new edge's parent; abort if `child` is
reached. Self-membership forbidden.

## Behavior — `Authorize`

```go
func Authorize(
    s        *store.Store,
    principal string,             // canonical, post-CanonicalSub
    action    string,             // "interact" | "admin" | "mcp:send" | ...
    scope     string,             // folder being acted upon
    claims    map[string]string,  // JWT claims (discord:guild, github:org)
    params    map[string]string,  // call args (jid, ...)
) bool
```

Evaluation:

1. Expand caller's principal set transitively via `acl_membership`
   (cycle-safe, exact-match query — `WHERE child IN (...)`).
2. Load `acl` rows whose `principal` matches any expanded principal.
   Pre-expansion converts `WHERE principal GLOB ?` (kills the index)
   into `WHERE principal IN (?, ?, ...)`. Stored globs
   (`discord:user/*`) handled by a second query path.
3. For each row: action covers requested action (lattice), scope
   glob-matches requested scope, predicate evaluates against claims,
   params glob-match call params.
4. **Deny wins**: any matching `deny` row → reject. Otherwise any
   matching `allow` → permit. No match for `mcp:*` → fall back to
   tier defaults from `grants.DeriveRules`. No match for
   `interact`/`admin` → deny.

`interact` and `admin` have **no tier default**: they are always
explicit grants (either an `acl` row or a route binding). Tier
defaults exist only for `mcp:*`. This sidesteps the "additive rows
can't revoke a tier default" problem cleanly. Route binding for
channel-bots: `routes` row binds `J → F`, gateway expands inbound
caller's set to include the room JID; one `acl(room_jid, interact,
F)` row grants the audience.

**Row-implication over rule-grammar deny.** If a row grants
`admin` on F and the derived rule list contains `!set_grants`, the
row wins (operator's explicit grant overrides the agent's self-imposed
deny). Rule grammar's `!action` syntax stays inside `grants/grants.go`
for the `mcp:*` derivation only.

## Token model — one shape for every actor

Every actor (human, agent, CLI, dashd) holds the same bearer-token
shape. No "agent auth" vs "user auth" branch at the auth boundary.

```
token = { principal, claims map[string]string, session_id, ttl }
```

| Actor | Issuer                                    | Principal        | Claims sourced from                                                | TTL                       |
| ----- | ----------------------------------------- | ---------------- | ------------------------------------------------------------------ | ------------------------- |
| Human | `auth/` after OAuth                       | `google:...`     | OAuth provider, refreshed at JWT renewal                           | 5 min, refresh kept alive |
| Agent | gated at spawn                            | `folder:<path>`  | spawn context (existing env vars at `container/runner.go:680-700`) | container lifetime        |
| CLI   | local-socket capability resolved to token | as authenticated | as authenticated                                                   | per-invocation            |

Agent claims (frozen at spawn): `folder`, `world`, `tier`, `parent`,
`is_root`, `delegate_depth` — all sourced from env vars gateway
already injects. Predicates match `claims[key]` uniformly:

```
predicate=tier=0                 -- only root agents
predicate=world=atlas            -- same-world only
predicate=discord:guild=837...   -- human is guild member
```

Claim freshness is per-actor: human claims refresh at JWT renewal
(1h default), agent claims never refresh (next spawn is the renewal).
Session-token refresh is server-side mint, same for both.

## Audit

`RenderACL(principal, scope)` returns the effective rule list for a
principal in scope — same evaluation as `Authorize`, exhaustive
instead of short-circuited. **Operator-only** by default; a principal
may render its own effective grants. Not a general agent tool —
spec 5/A's "agent finds out at failure site" carries over.

`acl_use_log(ts, principal, action, scope, allowed)` table keyed on
ts. Sampled at 100% for denied, 1% for allowed.

## Caching

`Authorize` is hot. Per-container row-set cache, life = container
lifetime. A monotonic `acl_version` watermark bumps on every write
to `acl`/`acl_membership`; container `SELECT acl_version` once per
tool call (single integer read) and refetches its row set on
mismatch. Revoke takes effect on next call, not next spawn —
satisfies spec 5/A's "next message" contract.

## Cross-spec impact

- **`specs/6/6` middleware**: `granted` + `grantedJID` collapse to one
  `gated(Authorize)` wrapper — JID flows through `params`.
- **`specs/5/A`**: subsumed. The `right` + `audience_predicate`
  columns proposed there become rows in `acl`. Route-as-auth survives
  as the room-JID principal pattern.
- **`specs/4/19`**: tier derivation stays in code; deny semantics
  live in the `effect='deny'` column.
- **`specs/6/5`**: `ScopePred` calls `Authorize`; the
  `<resource>:<verb>[:own_group]` scope shorthand mints `acl` rows.

## Bootstrap

`arizuko create` inserts two rows idempotently:

```sql
INSERT INTO acl (principal, action, scope, effect, granted_at)
  VALUES ('role:operator', '*', '**', 'allow', now)
  ON CONFLICT DO NOTHING;
INSERT INTO acl_membership (child, parent, added_at)
  VALUES ($OPERATOR_SUB, 'role:operator', now)
  ON CONFLICT DO NOTHING;
```

`OPERATOR_SUB` is read at create-time only — no runtime env-read.
Operator corrections via `membership.add` / `membership.remove` MCP tools;
emergency escape hatch is direct DB edit.

## Open questions

1. **Predicate grammar.** Single `key=value` glob, or boolean
   expression (`A AND B`)? Lean: single conjunction per row, multiple
   rows for disjunction.
2. **Membership freshness.** Discord/GitHub claims have TTL. Re-verify
   on each Authorize, or trust JWT until expiry? Lean: trust JWT;
   1h renewal is fast enough.
3. **`folder:` principal trust.** Can the operator grant
   `folder:atlas/eng admin atlas/**` (delegating admin to an agent)?
   Today the agent's capability is the unix socket. Lean: yes —
   `folder:` is a first-class principal.
4. **Anonymous-to-OAuth upgrade.** When `telegram:user/123` later
   OAuths, rewrite rows to canonical sub or evaluate both forms?
   Lean: insert an `acl_membership(telegram:user/123, google:...)`
   edge at link time; rows untouched. Membership expansion handles
   the rest.
5. **`acl` write scope.** Who may write `acl`? Lean: only `*`
   principal (operator) and folder-admin (`admin` at scope ⊇
   row.scope).

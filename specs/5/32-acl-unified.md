---
status: partial
depends: [5/17-openapi-mcp, 5/E-routd]
supersedes-in-part: [4/19-action-grants.md]
---

# Unified ACL — one primitive, three principals

> **Status (2026-08-05).** Partial. `auth.Authorize` is not the only reader of
> the table: `store.UserScopes` (`store/acl.go:200`) answers folder visibility
> for dashd and onbod with raw SQL that ignores the `action` column, deny-wins
> precedence, and wildcard principals. On the dashd/onbod read surfaces its
> result is the terminal 200/403 decision, so it is a second authz-adjacent
> decision path beside the one gate this spec makes canonical. BUGS `F5`.

Authorization is one row and one question:

```
(principal, action, scope, params, predicate, effect, grant_option) → allow | deny
```

Every actor — human, agent, channel room, role — is a principal of the same
shape, and every decision goes through one `Authorize`. Two tables: `acl` for
grants, `acl_membership` for identity-indirection (role membership, JID claims,
role hierarchy — one graph, three uses).

**Scope boundary.** This spec owns the row and the evaluator.
[`5/33`](33-paths-roles.md) owns the model decision — no tiers, path/role/
grant-option, delegation by subset-of-held. Nothing here derives authority from
path depth.

## The row

- **principal** — who is asking. Globbed segment-wise on `/` AND `:` (so
  `google:*` does not match `google:114/sub`).
- **action** — `interact`, `admin`, `mcp:<tool>`, `*`.
- **scope** — folder path or glob (`auth/acl.go:21` `matchPattern`; `**` crosses
  segments, `*` does not).
- **params** — optional `name=glob` predicates over call args (`jid=telegram:*`).
- **predicate** — optional claim condition (`discord:guild=G123`). Empty = none.
- **effect** — `allow` or `deny`. Deny wins.
- **grant_option** — `0|1`. `1` = the holder may re-delegate this row or a
  subset. The delegation axis, orthogonal to the action lattice's coverage.
  Added by `5/33`; see `auth/delegate.go`.

Schema: `routd/migrations/0007-acl.sql` (+ `0021` for `grant_option`);
`store/acl.go` is the reader/writer. Row counts are trivially small — hundreds,
not millions.

### Principal namespace

| Namespace         | Example                  | Meaning                                    |
| ----------------- | ------------------------ | ------------------------------------------ |
| OAuth sub         | `google:114019...`       | Canonical human sub (see below).           |
| Folder agent      | `folder:atlas/eng`       | The agent container at this folder.        |
| Platform identity | `telegram:user/123456`   | Channel-side identity, no OAuth yet.       |
| Room identity     | `discord:837.../1504...` | Channel/room JID — the route audience.     |
| Role              | `role:operator`          | Indirection; members via `acl_membership`. |
| Wildcard          | `**`, `google:*`         | Any principal / any sub in a namespace.    |

### Alias resolution — one account, one principal

**A principal in `acl` is a provider sub, never an account id.** `google:114...`
is the row key; there is no `user:` prefix and no account-level indirection in
the table. That is deliberate — channel identities (`telegram:user/123`) and
OAuth subs then share one namespace and one glob grammar.

It also means a person with several linked logins would otherwise be several
principals. `oauth_identities` (auth.db) records that they are one person;
authorization must consume that fact somewhere. **It is consumed at mint, not at
evaluation:** authd resolves the presented login to the account's canonical
provider sub and stamps THAT as the JWT `sub` (`authd/oauth.go` `dispatch`).
`auth.Authorize` sees an ordinary principal and does no resolution — no lookup,
no second identity path, no cross-daemon read of auth.db from routd.

This covers OAuth logins only. A channel JID (`discord:user/811...`) is not an
`oauth_identities` row and never resolves; it reaches a person's grants through
an `acl_membership` edge at pair time ([`5/31`](31-identity-pairing.md)). Two
mechanisms, because they answer different questions — "which account is this
login?" versus "whose channel identity is this?".

**The canonical sub is `auth_users.user_id`.** `dispatch` sets it to the FIRST
login's `<provider>:<providerSub>` and nothing rewrites it. Immutability is the
whole point: `acl` rows key on this value, so a canonical that moved would
silently strip an account of its grants.

- **Not earliest `linked_at`** — derivable with no schema change, but unlinking
  the earliest identity moves the canonical, and moving it silently transfers
  authority. Rejected on stability.
- **Not a new `canonical_sub` column** — `user_id` already carries exactly this
  fact. A second carrier of one fact is two carriers that drift.
- **Unlinking the canonical identity must be refused, loudly.** It cannot move
  the canonical (the column is immutable), but it would anchor the account to a
  login nobody holds. Re-designation is an operator action; there is no unlink
  path today, and this is the constraint the one that gets built must satisfy.

**What resolution costs.** The subject no longer says which login was presented,
and nothing downstream can recover it — proxyd stamps `X-User-Sub` from the
subject and `5/I`'s audit actor derives from that. The fact survives in authd's
own `audit_log`: an `authn`/`login` row whose `actor` is the account and whose
`resource` is the credential presented. Deliberately NOT a JWT claim —
`refresh_tokens` stores only the canonical sub, so a login claim would vanish on
the first refresh, and a claim that is present at login and absent 15 minutes
later is worse than no claim.

**Rejected: a sibling-subs union in the token.** The earlier design minted every
linked sub as a claim and folded them into `Caller.Extra` so `expandPrincipals`
would union them. It works, but it makes one person N principals at every
evaluation, spreads their grants across N rows, and leaves the union stale until
token expiry. Resolving to one canonical sub keeps one person one principal and
leaves this spec's evaluator untouched.

### Action lattice

```
*  ⊃  admin  ⊃  interact
*  ⊃  mcp:<tool>          admin ⊃ mcp:<tool>          mcp:* ⊃ mcp:<tool>
```

Implication is evaluated, not denormalized (`auth/authorize.go` `actionCovers`).
Granting `admin` is **not** equivalent to inserting one row per `mcp:<tool>` —
the lattice is the contract.

## Membership — roles, JID claims, channels

All three are `acl_membership` edges:

| Edge                                                   | What it expresses |
| ------------------------------------------------------ | ----------------- |
| `acl_membership(google:114alice, role:operator)`       | alice is operator |
| `acl_membership(discord:user/811..., google:114alice)` | JID claim         |
| `acl_membership(role:senior, role:operator)`           | role hierarchy    |

At message arrival the caller's principal set expands to include both the sender
JID and the room JID, plus everything transitively reachable from either
(`auth/authorize.go` `expandPrincipals`). The room JID carries the route's
baseline grants; transitive membership carries personal grants; one `acl` lookup
serves both. Cycle prevention is a transactional walk on write.

Postgres/IAM mapping is 1:1 — `GRANT role TO user` is an `acl_membership` row,
`GRANT perm TO role` is an `acl` row, an IAM group is `role:<name>`, an IAM
binding is the `acl` row itself.

**Seeded roles** are `role:operator` (`*` on `**`, WITH GRANT OPTION) and
`role:member` (the messaging floor) — both in `routd/migrations/` (`0022`,
`0023`). There are no per-depth bundles.

## Evaluation — `auth.Authorize` (`auth/authorize.go:25`)

Expand the caller's principals transitively; load exact-match rows plus wildcard
rows that match the expanded set; for each, check action-covers, scope-glob,
predicate against claims, and params against call args; **deny wins**; no match
= deny.

**There is no fallback.** An unmatched `mcp:*` used to fall back to
depth-derived defaults; `5/33` deleted that, so a missing grant denies loud
instead of silently defaulting (`auth/authorize_test.go:211`). `interact` and
`admin` were never depth-defaulted — they have always been explicit rows or a
route binding.

Route binding for channel bots: a `routes` row binds `J → F`, the inbound
caller's set expands to include the room JID, and one `acl(room_jid, interact,
F)` row grants the audience.

`auth.EffectiveActions` (`auth/authorize.go:86`) is the separate **visibility**
view for `tools/list` — does the caller hold this action at ANY scope. Deny rows
are scope-specific and do not hide a tool; the per-call `Authorize` still
enforces them.

## Bootstrap

`arizuko create` idempotently inserts the `role:operator` grant row and an
`acl_membership` edge binding the operator's OAuth sub to it. `OPERATOR_SUB` is
read at create time only — never at runtime. Corrections go through the
membership tools; the escape hatch is a direct DB edit.

## No caching, deliberately

`Authorize` reads `acl`/`acl_membership` on every call. The `acl_version`
watermark this spec once specified exists nowhere in Go or SQL, and the live
read is _stronger_ than the cache design would have been: revocation takes
effect on the **next tool call within a live turn**, not merely the next
message. `routd/revocation_live_test.go` pins that.

This is also why a brokered turn token must never carry an affirmative scope —
baked authority would outlive a revoked grant until token expiry, reintroducing
exactly the staleness the absent cache would have. If a cache is ever added,
next-call revocation is the contract it must preserve.

## Audit

Per-call auditing is `5/I`'s `audit_log`. The `acl_use_log` table and the
`RenderACL` renderer this spec once proposed were never built and are not
planned — the `acl` resource's list face already answers "what does this
principal effectively hold" (`5/33` decision 11).

## Rejected

- **OPA / Rego** (CNCF, Datalog-based policy). Our 80% case is
  `reply(jid=slack:*)`-shaped per-tool gating; a heavy embedded DSL is
  disproportionate and adds an authoring burden for operators. Reopen if
  cross-row conditions ("may `merge` only if it previously `reviewed` the same
  PR"), time-of-day rules, or operator-authored compliance policy appear.
  Adoption shapes and the deeper write-up: `memory/reference_opa.md`.
- **Denormalizing the action lattice** into one row per `mcp:<tool>` — loses the
  contract and multiplies rows on every grant.

## Cross-spec impact

- **[`5/33`](33-paths-roles.md)** — the model that sits on this row.
- **[`5/17`](17-openapi-mcp.md)** — the injected `Gate` at each resource site
  calls `Authorize`; resreg carries no auth policy of its own.

## Open questions

1. **Predicate grammar.** Single `key=value` glob, or boolean expression? Lean:
   one conjunction per row, multiple rows for disjunction.
2. **Membership freshness.** Discord/GitHub claims have TTL. Lean: trust the JWT
   until expiry; 1h renewal is fast enough.
3. **`acl` write scope.** Who may write `acl`? Today: root, plus any principal
   passing `auth.Delegate` (subset of held, WITH GRANT OPTION).

Closed and recorded elsewhere: `folder:` principal trust and the
anonymous-to-OAuth upgrade — decided by [`5/33`](33-paths-roles.md) (the agent
IS a first-class `acl` principal) and [`5/31`](31-identity-pairing.md) (an
`acl_membership` edge at pair time; rows untouched).

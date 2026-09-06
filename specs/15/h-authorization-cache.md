---
status: draft
depends: [../6/40-store-surface, ../6/22-proxyd-cut, ../5/33-paths-roles]
---

# specs/15/h — the authorization cache: resolve at mint, revoke from the issued set

`auth.Authorize` re-derives a principal's grants on every call, walking the
membership graph through `store.Ancestors` (`auth/authorize.go:134`, `:308`),
and `routd/sibling_db.go:312` walks it again to ask whether a caller is an
operator. This spec records the optimisation that moves that work to token-mint
time, and the reason it is not scheduled.

**It is an optimisation, not a simplification.** The walk still runs, once per
mint instead of once per call, and a revocation scan still runs when a
capability changes. Nothing leaves the store surface: `6/40` keeps the
reachability guard in its census either way, and portability is unchanged. What
this buys is latency on the authorize path, and nothing else.

## Why it is not needed yet

Measured 2026-09-06 on all three live instances. The membership graph has no
depth: 58 edges on krons pointing at exactly two parents, `role:operator` and
`role:member`, and **no parent is ever itself a child**, so the recursive query
never recurses. The one apparent DAG is an identity alias
(`telegram:user/1183985669 → google:114015…`) sharing a table with role
assignment, not a hierarchy.

A recursion that never recurses costs a single indexed lookup. There is nothing
to optimise.

## The shape, if it is ever needed

Most of the machinery exists.

- The access token already carries a **login-time scope snapshot** and the
  `arz/folder` claim (`authd/oauth.go`), and `proxyd` verifies it offline
  against a cached JWKS with no per-request hop.
- `authd` records what it issued. `refresh_tokens` holds 78 rows on the busiest
  live instance and none on the other two.
- `POST /dash/authd/revoke` kills one refresh family (`authd/dash.go:59`).
- `accessTTL` is 15 minutes.

So invalidation does not need the affected principal set computed at write
time, which is the hard part — "which principals does this `acl` write affect"
is itself a subtree question. It needs the opposite. Because the issued set is
recorded and small, a capability change **scans it** and revokes the tokens
carrying that capability; a scan over tens of rows costs nothing, and
capabilities change rarely. Where an immediate effect is not required a token
simply expires, which bounds any change at 15 minutes.

The one case that cannot wait is removing an operator's `**`. That is a single
revoked-token check per request against a table with tens of rows — a point
lookup, native to any engine, not a recursion.

## What blocks it

A decision, not machinery. [`6/22`](../6/22-proxyd-cut.md) step 3 refuses to
cache an authorization decision, on the grounds that a cached decision is a
revocation that does not take effect. Adopting this means replacing that rule
with the bound above: a revocation takes effect immediately for anything on the
revoked list, and within `accessTTL` for everything else. That is a weaker
guarantee stated honestly, and it needs sign-off rather than a patch.

## Reopen when

Any parent becomes a child — a membership edge whose parent is itself a member
of something — or `Ancestors` shows on a latency profile. Until one of those,
this is a cache for a query that already costs one lookup.

## Ties

`6/40` (the store surface; the reachability guard stays in its census
regardless) · `6/22` step 3 (the rule this replaces) · `5/33` (the two axes a
snapshot would have to carry).

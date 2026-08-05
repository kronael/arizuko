---
status: pointer
---

# Grants — pointer

Authorization in arizuko is one row, one question:

```
(principal, action, scope, params, predicate, effect, grant_option) → allow | deny
```

Two tables — `acl` (permissions) and `acl_membership` (identity
indirection) — and one function, `auth.Authorize`. Deny wins; an action
with no matching allow row is denied. There is no fallback and no
default derived from where a folder sits.

Default power is a seeded role, not a position: every folder is bound to
`role:member` at create, carrying the messaging verbs. Everything else —
`register_group`, `routes`, `network_*`, `schedule_*`, `observe_*`,
`invite_*`, token mint, `acl` itself — is an explicit `acl` row.
`role:operator` holds `*` on `**` WITH GRANT OPTION and roots every
delegation chain; root is a grant the operator invokes with `/root`, not
a folder anyone occupies.

A grant carries `grant_option`. Holding a row WITH GRANT OPTION is what
lets a principal delegate onward, and only a subset of what it already
holds — so authority strictly decreases down every chain
(`auth.Delegate`).

A channel identity (`telegram:user/123`) never appears in `acl`. It
acquires authority only by becoming a member of an account that has
some — the `acl_membership` edge — and the only way to write that edge
is identity pairing, where the account owner confirms in a browser
(`specs/5/31-identity-pairing.md`, threat model in `SECURITY.md`
§"Pairing: consent is the boundary").

## Canonical sources

- **Model**: [`specs/5/33-paths-roles.md`](specs/5/33-paths-roles.md) —
  what an identity carries, where default power comes from, how
  authority moves between principals.
- **Row + evaluator**: [`specs/5/32-acl-unified.md`](specs/5/32-acl-unified.md) —
  principal namespace, action lattice, evaluation order, bootstrap, audit.
- **Operator concepts**: `template/web/pub/arizuko/concepts/grants.html` —
  patterns (user-bot, channel-bot, hybrid), examples, anti-patterns.
- **Code**: `auth/authorize.go` (the single entry point),
  `auth/delegate.go` (`Delegate`, subset-of-held), `auth/acl.go`,
  `store/acl.go`, `store/membership.go`,
  `routd/migrations/0007-acl.sql`,
  `routd/migrations/0021-acl-grant-option.sql`,
  `routd/migrations/0022-seed-operator-grant-option.sql`,
  `routd/migrations/0023-4r-role-member.sql`.

## Related specs

- `specs/4/19-action-grants.md` — rule grammar (`!send`,
  `send(jid=telegram:*)`) used in a row's `params` predicates.
- `specs/5/17-openapi-mcp.md` — the injected `Gate` at each resource
  site; `resreg` carries no auth policy of its own.
- `specs/5/31-identity-pairing.md` — how a channel identity gets an
  `acl_membership` parent in the first place (`issue_pairing_link` /
  `unpair`).

The pointer is intentionally thin — there is one source of truth (the
specs), not a maintainer paraphrase.

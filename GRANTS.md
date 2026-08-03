---
status: pointer
---

# Grants — pointer

Authorization in arizuko is one row, one question:

```
(principal, action, scope, params, predicate, effect) → allow | deny
```

Two tables — `acl` (permissions) and `acl_membership` (identity
indirection) — and one function, `auth.Authorize`. Tier defaults stay
in code (`grants.DeriveRules`); operator overrides become `acl` rows.

A channel identity (`telegram:user/123`) never appears in `acl`. It
acquires authority only by becoming a member of an account that has
some — the `acl_membership` edge — and the only way to write that edge
is identity pairing, where the account owner confirms in a browser
(`specs/5/31-identity-pairing.md`, threat model in `SECURITY.md`
§"Pairing: consent is the boundary").

## Canonical sources

- **Spec**: [`specs/5/32-acl-unified.md`](specs/5/32-acl-unified.md) —
  the model, principal namespace, action lattice, evaluation order,
  bootstrap, audit.
- **Operator concepts**: `template/web/pub/concepts/grants.html` —
  patterns (user-bot, channel-bot, hybrid), examples, anti-patterns.
- **Code**: `auth/authorize.go` (the single entry point),
  `auth/policy.go` (`AuthorizeStructural` for hierarchy invariants),
  `store/acl.go`, `store/membership.go`,
  `store/migrations/0052-acl-unified.sql`,
  `store/migrations/0053-acl-cutover.sql`.

## Related specs

- `specs/3/5-tool-authorization.md` — tier × action defaults
  (consumed by `Authorize`'s fallback path).
- `specs/4/19-action-grants.md` — rule grammar (`!send`,
  `send(jid=telegram:*)`) used inside the tier-default rule list.
- `specs/5/6-middleware-pipeline.md` — MCP call-site wrapping
  (`routd(Authorize)`).
- `specs/5/31-identity-pairing.md` — how a channel identity gets an
  `acl_membership` parent in the first place (`issue_pairing_link` /
  `unpair`).

Earlier revisions of this file documented a 4-layer composition
(`groups` + `user_groups` + `routes` + `secrets`) that the v0.38.0
cutover collapsed. The pointer is intentionally thin — there is one
source of truth (the spec), not a maintainer paraphrase.

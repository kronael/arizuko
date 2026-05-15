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

## Canonical sources

- **Spec**: [`specs/6/9-acl-unified.md`](specs/6/9-acl-unified.md) —
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
- `specs/6/6-middleware-pipeline.md` — MCP call-site wrapping
  (`gated(Authorize)`).

Earlier revisions of this file documented a 4-layer composition
(`groups` + `user_groups` + `routes` + `secrets`) that the v0.38.0
cutover collapsed. The pointer is intentionally thin — there is one
source of truth (the spec), not a maintainer paraphrase.

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
`role:member` at create, carrying the messaging verbs and `disengage`. Everything else —
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
(threat model in `SECURITY.md` § Pairing: consent is the boundary).

## Where each fact lives

- **The model, the figure and every rule**:
  [`auth/README.md` § Authorization is one row and one
  question](auth/README.md#authorization-is-one-row-and-one-question) —
  the row, the three readers, the agent socket's two axes, exact-match
  readers, the magnitude gate.
- **The trust model**: [`SECURITY.md` § The sender cannot lend the agent
  authority](SECURITY.md#the-sender-cannot-lend-the-agent-authority) and
  § Pairing: consent is the boundary.
- **The operator's row reference**:
  `template/web/pub/arizuko/reference/grants.html` — lattice, params and
  predicates, holds and `/always`, delegation, worked examples.
- **Operator concepts**: `template/web/pub/arizuko/concepts/grants.html` —
  patterns (user-bot, channel-bot, hybrid) and anti-patterns.
- **Code**: `auth/authorize.go` (the single entry point),
  `auth/delegate.go` (`Delegate`, subset-of-held), `auth/acl.go`,
  `store/acl.go`, `store/membership.go`, and `store/seed.go`
  (`SeedRoutdFloor`, the seeded `role:member` and `role:operator` rows).

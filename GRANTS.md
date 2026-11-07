---
status: pointer
---

# Grants — pointer

Authorization in arizuko is one row, one question:

```
(principal, action, scope, params, predicate, effect, grant_option) → allow | deny
```

`auth.Policy.Allows` evaluates permissions from two layers: local `acl` rows
and the defaults in `auth/defaults.go`. Matching local rows decide first.
Defaults decide only when no local row matches. Deny wins within the
selected layer. No matching allow means refusal. Folder depth grants nothing.

`acl_membership` links principals to roles and accounts. The operator invokes
root authority with `/root`. Ordinary tool gates accept an elevated turn.
Rule mutations still pass `auth.CheckGrant` with the issuer's authority.

`CheckGrant` checks added allows and removals outside `hold:` against covering
authority with `grant_option`. Hold mutations require admin authority.
Adding a non-hold deny narrows access. Scope, params and predicate restrictions
also apply. `auth/README.md` states the containment rules.

A channel identity such as `telegram:user/123` can hold its own ACL rows.
Pairing links the identity to an account through `acl_membership`, so the
identity also receives the account's policy. The account owner confirms the
pairing in a browser. `SECURITY.md` states the consent boundary.

## Where each fact lives

- **The model, the figure and every rule**:
  [`auth/README.md` § Authorization is one row and one
  question](auth/README.md#authorization-is-one-row-and-one-question) —
  the row, the policy layers, the agent socket's two axes, isolated action
  readers, the magnitude gate.
- **The trust model**: [`SECURITY.md` § The sender cannot lend the agent
  authority](SECURITY.md#the-sender-cannot-lend-the-agent-authority) and
  § Pairing: consent is the boundary.
- **The operator's row reference**:
  `template/web/pub/arizuko/reference/grants.html` — lattice, params and
  predicates, holds and `/always`, delegation, worked examples.
- **Operator concepts**: `template/web/pub/arizuko/concepts/grants.html` —
  patterns (user-bot, channel-bot, hybrid) and anti-patterns.
- **Code**: `auth/authorize.go` — the policy evaluator;
  `auth/defaults.go` — the default policy; `auth/grant.go` — `CheckGrant`;
  `auth/acl.go`, `store/acl.go` and `store/membership.go`.

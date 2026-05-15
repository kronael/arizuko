---
status: docs
---

# Operator — design note

Canonical reference: `ARCHITECTURE.md ## Operator`.

Operator is **not a role flag or sentinel** — it is membership in
`role:operator` in the unified ACL. Any sub joined via
`acl_membership(<sub>, role:operator)` inherits the seeded row
`acl(role:operator, *, **, allow)`; `auth.Authorize` handles tier-0
visibility uniformly (see `auth/acl.go`, `store/acl.go`,
`store/membership.go`). There is no `groups.is_operator` flag, no
`router_state['operator_jid']` sentinel, and no nil-default routing
target.

Cross-group event notification (errors, scheduled health checks,
listener digests) resolves "where to send" by querying
`acl_membership` for `role:operator` members and routing into their
existing folders, not by seeding a flagged group.

What still needs design before any proactive-operator work ships:

- error/health-check trigger plumbing (`InsertSysMsg` from `gated`)
- dedup/rate-limit policy for error bursts
- listener-digest delivery format

These are mechanism questions; the addressing question is settled.

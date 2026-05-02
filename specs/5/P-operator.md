---
status: note
---

# Operator agent (proactive) — design note

Operator is **not a role or flag** — it is emergent from the user
ACL. Any user with a `**` row in `user_groups` is "the operator";
`auth.MatchGroups` handles tier-0 visibility uniformly (see
`auth/acl.go`, `store/auth.go`). There is no `groups.is_operator`
flag, no `router_state['operator_jid']` sentinel, and no
nil-default routing target.

Cross-group event notification (errors, scheduled health checks,
listener digests) resolves "where to send" by consulting
`user_groups` for `**`-grant holders and routing into their
existing folders, not by seeding a flagged group.

What still needs design before any proactive-operator work ships:

- error/health-check trigger plumbing (`InsertSysMsg` from `gated`)
- dedup/rate-limit policy for error bursts
- listener-digest delivery format

These are mechanism questions; the addressing question is settled.

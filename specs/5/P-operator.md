---
status: note
---

# Operator agent (proactive) — design note

Operator is **not a role or flag** — it is emergent from the user
ACL. Any user with a `**` row in `user_groups` is "the operator";
`MatchGroups` handles tier-0 visibility uniformly. There is no
`groups.is_operator` flag, no `router_state['operator_jid']`
sentinel, and no nil-default routing target.

The earlier draft proposed a single-row `is_operator` flag plus a
distinguished JID slot. That model was rejected — see
`feedback_operator_implicit` in project memory. Cross-group event
notification (errors, scheduled health checks, listener digests)
should resolve "where to send" by consulting `user_groups` for
`**`-grant holders and routing into their existing folders, not by
seeding a flagged group.

What still needs design before any proactive-operator work ships:

- error/health-check trigger plumbing (`InsertSysMsg` from `gated`)
- dedup/rate-limit policy for error bursts
- listener-digest delivery format

These are mechanism questions; the addressing question is settled.

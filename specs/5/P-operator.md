---
status: unshipped
---

# Operator agent (proactive)

A tier-0 group that monitors events and initiates messages to the
operator. Distinguished by `groups.is_operator` flag (single-row
constraint); operator JID in `router_state['operator_jid']`.

Triggers:

- **Error** — `gated` circuit breaker/persistent error → `InsertSysMsg`
  into operator group as `<error group="X">...</error>`.
- **Scheduled** — hourly health-check task with
  `<health_check>...</health_check>` prompt. Silence = success.
- **Listener digest** — future; listener groups post to operator
  instead of direct.

Agent uses existing `send_message` to reach operator JID on any
channel; default tier-0 grants cover the needed tools.
[feedback_operator_implicit] memory: operator may not even need this
flag — emergent from `**` grant. Reconcile before shipping.

Rationale: `notify/` is fire-and-forget strings; some events need
reasoning ("wake me at 3am?").

Unblockers: schema, `InsertSysMsg` plumbing, default SOUL.md, operator
seeding in `arizuko create`. Dedup/rate-limit policy for error bursts.

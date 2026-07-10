---
status: draft
---

# Agent code modification (staging)

Root agent (tier 0) can read gateway source via `/opt/arizuko/` but
cannot write. Let it propose changes to `/opt/arizuko-staging/`; operator
applies via `arizuko apply-staging <instance>` (diff → approve → apply →
rebuild).

Rationale: self-modification without live-patching risk.

Unblockers: decide auto-apply vs review, conflict resolution against
upstream, rebuild trigger, rollback path.

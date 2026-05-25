---
status: superseded
superseded_by: specs/4/9-acl-unified.md
---

# A-auth-consolidated (consolidated into 4/9-acl-unified 2026-05-25)

This spec was the design log that produced the unified ACL model.
Its content — three auth shapes (user-bot / channel-bot / hybrid),
the route-as-auth proposal, user-sub glob extension, OAuth
membership claims, and the decided open questions — fed into the
canonical [`specs/4/9-acl-unified.md`](../4/9-acl-unified.md).

4/9 carries the final design:

- The unified `acl` + `acl_membership` schema.
- `Authorize(principal, action, scope, params)` as the single gate.
- The two DECIDED open questions from this log:
  identification-vs-access separation; agent finds out at failure
  site.
- The user-sub glob mechanism (now part of `acl.scope` semantics).
- Route-as-auth survives as the room-JID principal pattern.

The earlier `5/28-mass-onboarding.md` and `5/29-acl.md` drafts this
spec originally superseded were folded into 4/9 the same way.

External references previously pointing here should resolve to
`specs/4/9-acl-unified.md`. This file remains as a historical
pointer.

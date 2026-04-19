---
status: draft
phase: later
depends: [28-acl]
---

# Slink → Scoped Auth Token

Deferred. Slink tokens work today as `groups.slink_token`.

When needed: migrate slink tokens into `auth_sessions` with
anonymous identity and group scope. Eliminates separate column
and codepath.

- Issued by bot via MCP tool
- Revoked by deleting from `auth_sessions`
- Scoped to specific groups (not user-wide)

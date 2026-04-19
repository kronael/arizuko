---
status: superseded
superseded_by: 4/23-topic-routing.md
---

# Agent Routing & Specialized Workers

Superseded by shipped topic routing (`4/23-topic-routing.md`) plus
`@agent` subgroup delegation. The "workers" idea is covered by child
groups spawned via `register_group` / prototype copy — each child is
its own container with its own session and skills.

What remains unbuilt:

- ML/keyword-based routing (classify intent → worker). Speculative.
- Worker image per subgroup (today all children inherit
  `CONTAINER_IMAGE`). Would need a `groups.image` column.

Neither has a concrete driver today.

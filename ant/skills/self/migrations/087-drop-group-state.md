# 087 — group state machinery removed

Groups no longer have a `state` (active/closed/archived). They exist
in the `groups` table until explicitly removed.

What changed:

- `groups.state`, `groups.spawn_ttl_days`, `groups.archive_closed_days`
  dropped from the schema.
- `timed` no longer runs the daily `cleanupSpawns` loop. Idle child
  groups are NOT auto-closed; closed groups are NOT auto-archived to
  tar.gz.
- `core.Group.State` field is gone. Code that read it always saw
  "active" anyway.

Operator impact: stale spawn rows accumulate. Remove with
`DELETE FROM groups WHERE folder = ?` (and `rm -rf groups/<folder>`
on disk) when needed.

This is informational — no behavior you call from inside the agent
changed. `register_group` still works the same way.

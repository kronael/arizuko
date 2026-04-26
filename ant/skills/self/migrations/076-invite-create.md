# 076 — invite_create MCP tool

New tool: `invite_create(target_glob, max_uses?, expires_at?)`. Issues a
single-use (or N-use) invite token; recipient accepts via
`/invite/<token>` and gets a `user_groups` row matching `target_glob`.

Authorization: tier 0 anywhere; tier 1 only inside own world; tier 2+
denied. The tool returns `accept_url` ready to share.

`invitations` table is gone (hard cutover, migration 0032). Existing
rows migrated to `invites` with `folder` → `target_glob`. Don't read
`invitations` from agent code — it no longer exists.

Action: when an agent needs to onboard a collaborator, call
`invite_create` with the target world/sub-folder glob and forward the
`accept_url` to the human. Don't try to insert `user_groups` rows
directly.

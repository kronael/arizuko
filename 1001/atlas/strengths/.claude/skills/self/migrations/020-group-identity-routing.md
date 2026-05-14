# 020 — Group identity env vars and routing defaults

## What changed

### New environment variables

The gateway now injects group identity into `settings.json`:

- `ARIZUKO_GROUP_NAME` — display name (e.g., "Atlas")
- `ARIZUKO_GROUP_FOLDER` — folder path (e.g., "atlas" or "atlas/support")
- `ARIZUKO_IS_WORLD_ADMIN` — "1" if tier 1 (world's main admin group)
- `ARIZUKO_CHAT_JID` — JID of the current chat session (set per invocation)

These join the existing `ARIZUKO_TIER`, `ARIZUKO_IS_ROOT`,
`ARIZUKO_ASSISTANT_NAME`.

### Routing actions updated

- `get_routes` — `jid` is now optional. Omit to get all routes,
  pass `$ARIZUKO_CHAT_JID` to filter by current chat.
- `add_route` — `jid` is required. Always use `$ARIZUKO_CHAT_JID`
  to target the current chat.
- `set_routes` action removed (never existed, was documented in error).

### IPC watcher

Now discovers nested group folders recursively. Groups like
`atlas/support` will have their IPC requests drained correctly.

## Action required

Update MEMORY.md if you stored assumptions about available env vars
or routing actions.

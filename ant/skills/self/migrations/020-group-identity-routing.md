# 020 — group identity env vars and routing

New env vars injected by the gateway:

- `ARIZUKO_GROUP_NAME` — display name
- `ARIZUKO_GROUP_FOLDER` — folder path (e.g. `atlas/support`)
- `ARIZUKO_IS_WORLD_ADMIN` — "1" if tier 1
- `ARIZUKO_CHAT_JID` — JID of the current chat (per invocation)

Routing:

- `get_routes` — `jid` optional; omit for all, pass `$ARIZUKO_CHAT_JID` to filter.
- `add_route` — `jid` required; use `$ARIZUKO_CHAT_JID` for current chat.
- `set_routes` removed (never existed).

IPC watcher now recurses into nested group folders (`atlas/support` etc).

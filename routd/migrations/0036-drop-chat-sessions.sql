-- Drop chat_sessions — the last plaintext route-token store.
--
-- 0029 declared it for dashd's chat portal, which recorded the RAW reusable
-- web: bearer so the portal could render a "continue" link. dashd was its only
-- writer and the portal is deleted (BUGS V2): webd's /me/chats is the one chat
-- surface, and it reads route_tokens' HASHES, minting through routd's
-- route_tokens resource. A table holding raw bearers with no reader is a
-- credential store with no owner.
--
-- No data loss: 0029 recorded zero rows fleet-wide and nothing has written
-- since. route_tokens keeps every token's hash, so no route is affected.
DROP INDEX IF EXISTS chat_sessions_folder;
DROP TABLE IF EXISTS chat_sessions;

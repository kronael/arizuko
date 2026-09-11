-- The chat portal's continue-link index is not a table in routd.db.
--
-- It recorded the RAW reusable `web:` bearer so a portal could render a
-- "continue" link, which is a credential store with no owner; webd's /me/chats
-- is the one chat surface and it reads route_tokens' HASHES, minting through
-- routd's route_tokens resource. 0036 drops what a database may still carry, and
-- this chain no longer builds it: zero rows fleet-wide, so nothing is lost.
SELECT 1;

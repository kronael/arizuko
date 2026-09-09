-- Tail of the typed-JID cutover: chat_reply_state.jid, the JID-shaped column
-- 0042 does not reach. Same kind-discriminator semantics as 0042. Idempotent: a
-- row already in typed shape (containing `/`) is skipped by every UPDATE.
--
-- routes.match and scheduled_tasks.chat_jid are NOT here. A rule's match is a
-- fragment of its own coordinate and a task's chat_jid is an attribute of an
-- item, and neither `routes` nor `scheduled_tasks` is a table this chain
-- creates: the driver makes each one, from the declaration that sits beside the
-- code writing it (store.EnsureEntities, spec 6/37 step 7). A pre-move
-- database's rules and tasks are rewritten by `arizuko migrate-routd`, which
-- reads the tables this chain left behind.

-- ============================================================================
-- chat_reply_state.jid
-- ============================================================================
UPDATE chat_reply_state SET jid = 'telegram:user/' || substr(jid, 10)
  WHERE jid LIKE 'telegram:%'
    AND jid NOT LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%'
    AND substr(jid, 10) GLOB '[0-9]*';

UPDATE chat_reply_state SET jid = 'telegram:group/' || substr(jid, 11)
  WHERE jid LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%';

UPDATE chat_reply_state SET jid = 'discord:dm/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%'
    AND jid IN (SELECT jid FROM chats WHERE is_group = 0);

UPDATE chat_reply_state SET jid = 'discord:_/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%';

UPDATE chat_reply_state SET jid = 'mastodon:account/' || substr(jid, 10)
  WHERE jid LIKE 'mastodon:%'
    AND jid NOT LIKE 'mastodon:account/%'
    AND jid NOT LIKE 'mastodon:status/%';

UPDATE chat_reply_state SET jid = 'reddit:comment/' || substr(jid, 11) WHERE jid LIKE 'reddit:t1_%';
UPDATE chat_reply_state SET jid = 'reddit:user/' || substr(jid, 11) WHERE jid LIKE 'reddit:t2_%';
UPDATE chat_reply_state SET jid = 'reddit:submission/' || substr(jid, 11) WHERE jid LIKE 'reddit:t3_%';

UPDATE chat_reply_state SET jid = 'bluesky:user/' || replace(substr(jid, 9), ':', '%3A')
  WHERE jid LIKE 'bluesky:%' AND jid NOT LIKE 'bluesky:user/%';

UPDATE chat_reply_state SET jid = 'linkedin:user/' || substr(jid, 10)
  WHERE jid LIKE 'linkedin:%' AND jid NOT LIKE 'linkedin:user/%';

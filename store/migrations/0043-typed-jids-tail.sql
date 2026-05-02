-- Tail of the typed-JID cutover: rewrite JID-shaped values that 0042 missed.
--   - routes.match `room=` predicates (chat_jid= and sender= were done in 0042)
--   - scheduled_tasks.chat_jid
--   - chat_reply_state.jid
-- Same kind-discriminator semantics as 0042. Idempotent: a row already in
-- typed shape (containing `/`) is skipped by every UPDATE.

-- ============================================================================
-- routes.match `room=` predicates
-- ============================================================================
-- Telegram: routes are written as `room=<id>` where <id> is the bare numeric
-- chat ID (negative = group/supergroup, positive = user). After 0042 the
-- platform layer emits typed `telegram:user/<id>` or `telegram:group/<id>`,
-- and JidRoom() returns `user/<id>` / `group/<id>`. Rewrite single-predicate
-- `room=<digits>` matches accordingly.

UPDATE routes SET match = 'room=group/' || substr(match, 7)
  WHERE match GLOB 'room=-[0-9]*'
    AND substr(match, 7) NOT GLOB '*[^0-9]*';

UPDATE routes SET match = 'room=user/' || substr(match, 6)
  WHERE match GLOB 'room=[0-9]*'
    AND substr(match, 6) NOT GLOB '*[^0-9]*';

-- Reddit thing-prefix room= predicates (defensive; no current deployments use
-- them but the rewrite is well-defined).
UPDATE routes SET match = 'room=comment/' || substr(match, 11)
  WHERE match GLOB 'room=t1_*';
UPDATE routes SET match = 'room=user/' || substr(match, 11)
  WHERE match GLOB 'room=t2_*';
UPDATE routes SET match = 'room=submission/' || substr(match, 11)
  WHERE match GLOB 'room=t3_*';

-- ============================================================================
-- scheduled_tasks.chat_jid
-- ============================================================================
UPDATE scheduled_tasks SET chat_jid = 'telegram:user/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'telegram:%'
    AND chat_jid NOT LIKE 'telegram:-%'
    AND chat_jid NOT LIKE 'telegram:%/%'
    AND substr(chat_jid, 10) GLOB '[0-9]*';

UPDATE scheduled_tasks SET chat_jid = 'telegram:group/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'telegram:-%'
    AND chat_jid NOT LIKE 'telegram:%/%';

UPDATE scheduled_tasks SET chat_jid = 'discord:dm/' || substr(chat_jid, 9)
  WHERE chat_jid LIKE 'discord:%'
    AND chat_jid NOT LIKE 'discord:%/%'
    AND chat_jid IN (SELECT jid FROM chats WHERE is_group = 0);

UPDATE scheduled_tasks SET chat_jid = 'discord:_/' || substr(chat_jid, 9)
  WHERE chat_jid LIKE 'discord:%'
    AND chat_jid NOT LIKE 'discord:%/%';

UPDATE scheduled_tasks SET chat_jid = 'mastodon:account/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'mastodon:%'
    AND chat_jid NOT LIKE 'mastodon:account/%'
    AND chat_jid NOT LIKE 'mastodon:status/%';

UPDATE scheduled_tasks SET chat_jid = 'reddit:comment/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t1_%';
UPDATE scheduled_tasks SET chat_jid = 'reddit:user/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t2_%';
UPDATE scheduled_tasks SET chat_jid = 'reddit:submission/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t3_%';

UPDATE scheduled_tasks SET chat_jid = 'bluesky:user/' || replace(substr(chat_jid, 9), ':', '%3A')
  WHERE chat_jid LIKE 'bluesky:%'
    AND chat_jid NOT LIKE 'bluesky:user/%'
    AND chat_jid NOT LIKE 'bluesky:post/%';

UPDATE scheduled_tasks SET chat_jid = 'linkedin:user/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'linkedin:%'
    AND chat_jid NOT LIKE 'linkedin:user/%'
    AND chat_jid NOT LIKE 'linkedin:post/%';

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

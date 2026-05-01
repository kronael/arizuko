-- Typed JIDs: hard-cutover rewrite of every JID-shaped value to put a
-- kind discriminator in the first path segment. After this migration:
--
--   telegram:<id>           → telegram:user/<id>     (id positive)
--                              telegram:group/<|id|>  (id negative; sign hack dropped)
--   discord:<channel>       → discord:dm/<channel>   (chats.is_group = 0)
--                              discord:_/<channel>    (chats.is_group = 1; guild_id unknown)
--   mastodon:<acct>         → mastodon:account/<acct>
--   reddit:t1_<id>          → reddit:comment/<id>
--   reddit:t2_<id>          → reddit:user/<id>
--   reddit:t3_<id>          → reddit:submission/<id>
--   web:<folder>            unchanged (folder-keyed identity layer)
--   bluesky:did:plc:<rest>  → bluesky:user/<percent-encoded>
--   email:<addr>            → email:address/<addr>
--   linkedin:<urn>          → linkedin:user/<urn>
--   whatsapp:<id>@<server>  unchanged (already shape-compliant)
--   twitter:tweet/<id>      unchanged (already shape-compliant)
--   twitter:dm/<id>         unchanged
--   twitter:user/<id>       unchanged
--
-- Rewrite is applied to every JID-shaped column in lockstep:
--   messages.chat_jid, messages.sender, messages.reply_to_sender
--   chats.jid (PK; agent_cursor follows because it's a column on chats)
--   user_jids.jid
--   grants.jid
--   onboarding.jid
--   routes.match (chat_jid=/sender= clauses)

-- ============================================================================
-- TELEGRAM: split signed integer into user/group kinds; drop sign bit.
-- ============================================================================
-- Helper: telegram:<digits> (positive) → telegram:user/<digits>
-- Telegram chat IDs: positive = user (DM), negative = group/supergroup/channel.
-- Supergroups/channels carry a -100... prefix; we drop the sign and keep the
-- digits as-is (the number identifies the chat regardless of the prefix).

UPDATE messages SET chat_jid = 'telegram:user/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'telegram:%'
    AND chat_jid NOT LIKE 'telegram:-%'
    AND chat_jid NOT LIKE 'telegram:%/%'
    AND substr(chat_jid, 10) GLOB '[0-9]*';

UPDATE messages SET chat_jid = 'telegram:group/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'telegram:-%'
    AND chat_jid NOT LIKE 'telegram:%/%';

UPDATE messages SET sender = 'telegram:user/' || substr(sender, 10)
  WHERE sender LIKE 'telegram:%'
    AND sender NOT LIKE 'telegram:-%'
    AND sender NOT LIKE 'telegram:%/%'
    AND substr(sender, 10) GLOB '[0-9]*';

UPDATE messages SET sender = 'telegram:group/' || substr(sender, 11)
  WHERE sender LIKE 'telegram:-%'
    AND sender NOT LIKE 'telegram:%/%';

UPDATE messages SET reply_to_sender = 'telegram:user/' || substr(reply_to_sender, 10)
  WHERE reply_to_sender LIKE 'telegram:%'
    AND reply_to_sender NOT LIKE 'telegram:-%'
    AND reply_to_sender NOT LIKE 'telegram:%/%'
    AND substr(reply_to_sender, 10) GLOB '[0-9]*';

UPDATE messages SET reply_to_sender = 'telegram:group/' || substr(reply_to_sender, 11)
  WHERE reply_to_sender LIKE 'telegram:-%'
    AND reply_to_sender NOT LIKE 'telegram:%/%';

UPDATE chats SET jid = 'telegram:user/' || substr(jid, 10)
  WHERE jid LIKE 'telegram:%'
    AND jid NOT LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%'
    AND substr(jid, 10) GLOB '[0-9]*';

UPDATE chats SET jid = 'telegram:group/' || substr(jid, 11)
  WHERE jid LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%';

UPDATE user_jids SET jid = 'telegram:user/' || substr(jid, 10)
  WHERE jid LIKE 'telegram:%'
    AND jid NOT LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%'
    AND substr(jid, 10) GLOB '[0-9]*';

UPDATE user_jids SET jid = 'telegram:group/' || substr(jid, 11)
  WHERE jid LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%';

UPDATE grants SET jid = 'telegram:user/' || substr(jid, 10)
  WHERE jid LIKE 'telegram:%'
    AND jid NOT LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%'
    AND substr(jid, 10) GLOB '[0-9]*';

UPDATE grants SET jid = 'telegram:group/' || substr(jid, 11)
  WHERE jid LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%';

UPDATE onboarding SET jid = 'telegram:user/' || substr(jid, 10)
  WHERE jid LIKE 'telegram:%'
    AND jid NOT LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%'
    AND substr(jid, 10) GLOB '[0-9]*';

UPDATE onboarding SET jid = 'telegram:group/' || substr(jid, 11)
  WHERE jid LIKE 'telegram:-%'
    AND jid NOT LIKE 'telegram:%/%';

-- ============================================================================
-- DISCORD: split via chats.is_group. DMs → discord:dm/<channel>;
-- guild channels → discord:_/<channel> (placeholder; legacy rows have no
-- guild_id, new inbound from discd MUST emit discord:<guild>/<channel>).
-- ============================================================================
UPDATE messages SET chat_jid = 'discord:dm/' || substr(chat_jid, 9)
  WHERE chat_jid LIKE 'discord:%'
    AND chat_jid NOT LIKE 'discord:%/%'
    AND chat_jid IN (SELECT jid FROM chats WHERE is_group = 0);

UPDATE messages SET chat_jid = 'discord:_/' || substr(chat_jid, 9)
  WHERE chat_jid LIKE 'discord:%'
    AND chat_jid NOT LIKE 'discord:%/%';

UPDATE messages SET sender = 'discord:user/' || substr(sender, 9)
  WHERE sender LIKE 'discord:%'
    AND sender NOT LIKE 'discord:%/%';

UPDATE messages SET reply_to_sender = 'discord:user/' || substr(reply_to_sender, 9)
  WHERE reply_to_sender LIKE 'discord:%'
    AND reply_to_sender NOT LIKE 'discord:%/%';

UPDATE chats SET jid = 'discord:dm/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%'
    AND is_group = 0;

UPDATE chats SET jid = 'discord:_/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%';

UPDATE user_jids SET jid = 'discord:user/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%';

UPDATE grants SET jid = 'discord:user/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%';

UPDATE onboarding SET jid = 'discord:_/' || substr(jid, 9)
  WHERE jid LIKE 'discord:%'
    AND jid NOT LIKE 'discord:%/%';

-- ============================================================================
-- MASTODON: drop host (single-instance per arizuko deployment); shape is
-- account/<id> for senders/recipients.
-- ============================================================================
UPDATE messages SET chat_jid = 'mastodon:account/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'mastodon:%'
    AND chat_jid NOT LIKE 'mastodon:account/%'
    AND chat_jid NOT LIKE 'mastodon:status/%';

UPDATE messages SET sender = 'mastodon:account/' || substr(sender, 10)
  WHERE sender LIKE 'mastodon:%'
    AND sender NOT LIKE 'mastodon:account/%'
    AND sender NOT LIKE 'mastodon:status/%';

UPDATE messages SET reply_to_sender = 'mastodon:account/' || substr(reply_to_sender, 10)
  WHERE reply_to_sender LIKE 'mastodon:%'
    AND reply_to_sender NOT LIKE 'mastodon:account/%'
    AND reply_to_sender NOT LIKE 'mastodon:status/%';

UPDATE chats SET jid = 'mastodon:account/' || substr(jid, 10)
  WHERE jid LIKE 'mastodon:%'
    AND jid NOT LIKE 'mastodon:account/%'
    AND jid NOT LIKE 'mastodon:status/%';

UPDATE user_jids SET jid = 'mastodon:account/' || substr(jid, 10)
  WHERE jid LIKE 'mastodon:%'
    AND jid NOT LIKE 'mastodon:account/%'
    AND jid NOT LIKE 'mastodon:status/%';

UPDATE grants SET jid = 'mastodon:account/' || substr(jid, 10)
  WHERE jid LIKE 'mastodon:%'
    AND jid NOT LIKE 'mastodon:account/%'
    AND jid NOT LIKE 'mastodon:status/%';

UPDATE onboarding SET jid = 'mastodon:account/' || substr(jid, 10)
  WHERE jid LIKE 'mastodon:%'
    AND jid NOT LIKE 'mastodon:account/%'
    AND jid NOT LIKE 'mastodon:status/%';

-- ============================================================================
-- REDDIT: t1_/t2_/t3_ thing-prefix → kind discriminator.
-- ============================================================================
UPDATE messages SET chat_jid = 'reddit:comment/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t1_%';
UPDATE messages SET chat_jid = 'reddit:user/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t2_%';
UPDATE messages SET chat_jid = 'reddit:submission/' || substr(chat_jid, 11)
  WHERE chat_jid LIKE 'reddit:t3_%';

UPDATE messages SET sender = 'reddit:comment/' || substr(sender, 11)
  WHERE sender LIKE 'reddit:t1_%';
UPDATE messages SET sender = 'reddit:user/' || substr(sender, 11)
  WHERE sender LIKE 'reddit:t2_%';
UPDATE messages SET sender = 'reddit:submission/' || substr(sender, 11)
  WHERE sender LIKE 'reddit:t3_%';

UPDATE messages SET reply_to_sender = 'reddit:comment/' || substr(reply_to_sender, 11)
  WHERE reply_to_sender LIKE 'reddit:t1_%';
UPDATE messages SET reply_to_sender = 'reddit:user/' || substr(reply_to_sender, 11)
  WHERE reply_to_sender LIKE 'reddit:t2_%';
UPDATE messages SET reply_to_sender = 'reddit:submission/' || substr(reply_to_sender, 11)
  WHERE reply_to_sender LIKE 'reddit:t3_%';

UPDATE chats SET jid = 'reddit:comment/' || substr(jid, 11) WHERE jid LIKE 'reddit:t1_%';
UPDATE chats SET jid = 'reddit:user/' || substr(jid, 11) WHERE jid LIKE 'reddit:t2_%';
UPDATE chats SET jid = 'reddit:submission/' || substr(jid, 11) WHERE jid LIKE 'reddit:t3_%';

UPDATE user_jids SET jid = 'reddit:user/' || substr(jid, 11) WHERE jid LIKE 'reddit:t2_%';

UPDATE grants SET jid = 'reddit:user/' || substr(jid, 11) WHERE jid LIKE 'reddit:t2_%';

UPDATE onboarding SET jid = 'reddit:user/' || substr(jid, 11) WHERE jid LIKE 'reddit:t2_%';

-- ============================================================================
-- WEB: stored as `web:<folder>` today (folder name doubles as the chat
-- ID for the slink hub). NOT migrated to `web:slink/...` or
-- `web:user/...` because the current code treats `web:<folder>` as a
-- folder reference. New typed forms apply when the web stack splits
-- token-vs-sub identity.
-- ============================================================================

-- ============================================================================
-- BLUESKY: percent-encode the embedded `:` in `did:plc:<rest>`.
-- New shape: bluesky:user/did%3Aplc%3A<rest>
-- ============================================================================
UPDATE messages SET chat_jid = 'bluesky:user/' || replace(substr(chat_jid, 9), ':', '%3A')
  WHERE chat_jid LIKE 'bluesky:%'
    AND chat_jid NOT LIKE 'bluesky:user/%'
    AND chat_jid NOT LIKE 'bluesky:post/%';

UPDATE messages SET sender = 'bluesky:user/' || replace(substr(sender, 9), ':', '%3A')
  WHERE sender LIKE 'bluesky:%'
    AND sender NOT LIKE 'bluesky:user/%'
    AND sender NOT LIKE 'bluesky:post/%';

UPDATE messages SET reply_to_sender = 'bluesky:user/' || replace(substr(reply_to_sender, 9), ':', '%3A')
  WHERE reply_to_sender LIKE 'bluesky:%'
    AND reply_to_sender NOT LIKE 'bluesky:user/%'
    AND reply_to_sender NOT LIKE 'bluesky:post/%';

UPDATE chats SET jid = 'bluesky:user/' || replace(substr(jid, 9), ':', '%3A')
  WHERE jid LIKE 'bluesky:%'
    AND jid NOT LIKE 'bluesky:user/%'
    AND jid NOT LIKE 'bluesky:post/%';

UPDATE user_jids SET jid = 'bluesky:user/' || replace(substr(jid, 9), ':', '%3A')
  WHERE jid LIKE 'bluesky:%' AND jid NOT LIKE 'bluesky:user/%';

UPDATE grants SET jid = 'bluesky:user/' || replace(substr(jid, 9), ':', '%3A')
  WHERE jid LIKE 'bluesky:%' AND jid NOT LIKE 'bluesky:user/%';

UPDATE onboarding SET jid = 'bluesky:user/' || replace(substr(jid, 9), ':', '%3A')
  WHERE jid LIKE 'bluesky:%' AND jid NOT LIKE 'bluesky:user/%';

-- ============================================================================
-- EMAIL: address (sender) shape. Existing messages.chat_jid uses thread IDs
-- (email:<message_id>); leave unchanged for now since `email:thread/...` and
-- `email:address/...` differ only in kind discriminator and current code
-- writes `email:` directly. Mark senders only; chat_jid migration deferred
-- until emaid emits the new shape on inbound.
-- ============================================================================
UPDATE messages SET sender = 'email:address/' || substr(sender, 7)
  WHERE sender LIKE 'email:%'
    AND sender NOT LIKE 'email:address/%'
    AND sender NOT LIKE 'email:thread/%';

UPDATE messages SET reply_to_sender = 'email:address/' || substr(reply_to_sender, 7)
  WHERE reply_to_sender LIKE 'email:%'
    AND reply_to_sender NOT LIKE 'email:address/%'
    AND reply_to_sender NOT LIKE 'email:thread/%';

UPDATE user_jids SET jid = 'email:address/' || substr(jid, 7)
  WHERE jid LIKE 'email:%'
    AND jid NOT LIKE 'email:address/%';

UPDATE grants SET jid = 'email:address/' || substr(jid, 7)
  WHERE jid LIKE 'email:%'
    AND jid NOT LIKE 'email:address/%';

-- ============================================================================
-- LINKEDIN: <urn> → user/<urn>
-- ============================================================================
UPDATE messages SET chat_jid = 'linkedin:user/' || substr(chat_jid, 10)
  WHERE chat_jid LIKE 'linkedin:%' AND chat_jid NOT LIKE 'linkedin:user/%' AND chat_jid NOT LIKE 'linkedin:post/%';

UPDATE messages SET sender = 'linkedin:user/' || substr(sender, 10)
  WHERE sender LIKE 'linkedin:%' AND sender NOT LIKE 'linkedin:user/%' AND sender NOT LIKE 'linkedin:post/%';

UPDATE messages SET reply_to_sender = 'linkedin:user/' || substr(reply_to_sender, 10)
  WHERE reply_to_sender LIKE 'linkedin:%' AND reply_to_sender NOT LIKE 'linkedin:user/%' AND reply_to_sender NOT LIKE 'linkedin:post/%';

UPDATE chats SET jid = 'linkedin:user/' || substr(jid, 10)
  WHERE jid LIKE 'linkedin:%' AND jid NOT LIKE 'linkedin:user/%' AND jid NOT LIKE 'linkedin:post/%';

UPDATE user_jids SET jid = 'linkedin:user/' || substr(jid, 10)
  WHERE jid LIKE 'linkedin:%' AND jid NOT LIKE 'linkedin:user/%';

UPDATE grants SET jid = 'linkedin:user/' || substr(jid, 10)
  WHERE jid LIKE 'linkedin:%' AND jid NOT LIKE 'linkedin:user/%';

-- ============================================================================
-- ROUTES.MATCH: rewrite chat_jid= and sender= predicates that contain legacy
-- JID patterns. We rewrite the literal-prefix forms to the new
-- kind-discriminator forms; leave any operator-edited globs alone (they'll
-- need manual review and re-issuance).
-- ============================================================================
-- Discord: chat_jid=discord:* becomes chat_jid=discord:*/* (* per segment).
-- Operators who routed by legacy guild-less form lose specificity but keep
-- coverage. New rules they add post-migration use the typed form.
UPDATE routes SET match = replace(match, 'chat_jid=discord:*', 'chat_jid=discord:*/*')
  WHERE match LIKE '%chat_jid=discord:*%'
    AND match NOT LIKE '%chat_jid=discord:*/*%';

-- Telegram: sender=telegram:* keeps shape (the * matches user/<id> too in
-- path.Match since path.Match treats * as "any non-/" — telegram:*  matches
-- only telegram:user_or_group_with_no_slashes which is now empty). Rewrite
-- to telegram:*/* to keep semantics.
UPDATE routes SET match = replace(match, 'chat_jid=telegram:*', 'chat_jid=telegram:*/*')
  WHERE match LIKE '%chat_jid=telegram:*%'
    AND match NOT LIKE '%chat_jid=telegram:*/*%';
UPDATE routes SET match = replace(match, 'sender=telegram:*', 'sender=telegram:*/*')
  WHERE match LIKE '%sender=telegram:*%'
    AND match NOT LIKE '%sender=telegram:*/*%';

-- Mastodon, reddit, web, bluesky, linkedin: same trick.
UPDATE routes SET match = replace(match, 'chat_jid=mastodon:*', 'chat_jid=mastodon:*/*')
  WHERE match LIKE '%chat_jid=mastodon:*%' AND match NOT LIKE '%chat_jid=mastodon:*/*%';
UPDATE routes SET match = replace(match, 'sender=mastodon:*', 'sender=mastodon:*/*')
  WHERE match LIKE '%sender=mastodon:*%' AND match NOT LIKE '%sender=mastodon:*/*%';

UPDATE routes SET match = replace(match, 'chat_jid=reddit:*', 'chat_jid=reddit:*/*')
  WHERE match LIKE '%chat_jid=reddit:*%' AND match NOT LIKE '%chat_jid=reddit:*/*%';
UPDATE routes SET match = replace(match, 'sender=reddit:*', 'sender=reddit:*/*')
  WHERE match LIKE '%sender=reddit:*%' AND match NOT LIKE '%sender=reddit:*/*%';

UPDATE routes SET match = replace(match, 'chat_jid=bluesky:*', 'chat_jid=bluesky:*/*')
  WHERE match LIKE '%chat_jid=bluesky:*%' AND match NOT LIKE '%chat_jid=bluesky:*/*%';

UPDATE routes SET match = replace(match, 'chat_jid=linkedin:*', 'chat_jid=linkedin:*/*')
  WHERE match LIKE '%chat_jid=linkedin:*%' AND match NOT LIKE '%chat_jid=linkedin:*/*%';

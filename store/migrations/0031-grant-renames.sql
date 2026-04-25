-- Verb taxonomy rename: send_message → send, send_reply → reply.
--
-- grant_rules.rules is a JSON array of strings (per store/grants.go).
-- Stored rules look like: "send_message", "send_message(jid=tg:*)",
-- "!send_reply", etc. We rewrite the substring globally; collisions
-- ("send" → "send", or pre-existing "send" rules) are idempotent.
--
-- Order matters: rewrite send_reply BEFORE send_message so the longer
-- token is consumed first (otherwise send_message → send would later
-- mangle send_reply → send_reply unchanged, which is fine, but
-- doing send_reply first keeps the diff minimal).
UPDATE grant_rules
   SET rules = REPLACE(REPLACE(rules, 'send_reply', 'reply'), 'send_message', 'send')
 WHERE rules LIKE '%send_reply%' OR rules LIKE '%send_message%';

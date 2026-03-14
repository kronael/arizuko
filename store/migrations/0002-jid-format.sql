-- Normalize JID format: add channel prefix where missing

UPDATE chats SET jid = 'telegram:' || jid
  WHERE jid GLOB '[0-9]*' AND channel = 'telegram';

UPDATE messages SET chat_jid = 'telegram:' || chat_jid
  WHERE chat_jid GLOB '[0-9]*'
  AND EXISTS (
    SELECT 1 FROM chats
    WHERE chats.jid = 'telegram:' || messages.chat_jid
    AND chats.channel = 'telegram'
  );

UPDATE registered_groups SET jid = 'telegram:' || jid
  WHERE jid GLOB '[0-9]*';

UPDATE chats SET jid = 'whatsapp:' || jid
  WHERE jid NOT LIKE '%:%' AND channel = 'whatsapp';

UPDATE chats SET jid = 'discord:' || jid
  WHERE jid NOT LIKE '%:%' AND channel = 'discord';

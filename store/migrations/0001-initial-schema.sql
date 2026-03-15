-- Initial schema: all core tables at baseline state

CREATE TABLE IF NOT EXISTS chats (
  jid TEXT PRIMARY KEY,
  name TEXT,
  channel TEXT,
  is_group INTEGER DEFAULT 0,
  last_message_time TEXT,
  errored INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  chat_jid TEXT NOT NULL,
  sender TEXT NOT NULL,
  sender_name TEXT,
  content TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  is_from_me INTEGER DEFAULT 0,
  is_bot_message INTEGER DEFAULT 0,
  forwarded_from TEXT,
  reply_to_text TEXT,
  reply_to_sender TEXT
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, timestamp);

CREATE TABLE IF NOT EXISTS registered_groups (
  jid TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  folder TEXT NOT NULL,
  trigger_word TEXT NOT NULL,
  added_at TEXT NOT NULL,
  container_config TEXT,
  requires_trigger INTEGER DEFAULT 1,
  slink_token TEXT,
  parent TEXT,
  routing_rules TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
  group_folder TEXT PRIMARY KEY,
  session_id TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_folder TEXT NOT NULL,
  session_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  result TEXT,
  error TEXT,
  message_count INTEGER
);

CREATE TABLE IF NOT EXISTS system_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id TEXT NOT NULL,
  origin TEXT NOT NULL,
  event TEXT NOT NULL,
  attrs TEXT,
  body TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduled_tasks (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL,
  cron TEXT,
  next_run TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_next ON scheduled_tasks(status, next_run);

CREATE TABLE IF NOT EXISTS router_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_users (
  id INTEGER PRIMARY KEY,
  sub TEXT UNIQUE NOT NULL,
  username TEXT UNIQUE NOT NULL,
  hash TEXT NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_sub TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS email_threads (
  thread_id TEXT PRIMARY KEY,
  chat_jid TEXT NOT NULL,
  subject TEXT,
  last_message_id TEXT
);

CREATE TABLE IF NOT EXISTS routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  jid TEXT NOT NULL,
  seq INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT 'default',
  match TEXT,
  target TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_routes_jid_seq ON routes(jid, seq);

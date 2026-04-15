-- Add token-based auth to onboarding + user_jids table

ALTER TABLE onboarding ADD COLUMN token TEXT;
ALTER TABLE onboarding ADD COLUMN token_expires TEXT;
ALTER TABLE onboarding ADD COLUMN user_sub TEXT;

CREATE TABLE IF NOT EXISTS user_jids (
    user_sub TEXT NOT NULL,
    jid      TEXT NOT NULL UNIQUE,
    claimed  TEXT NOT NULL,
    PRIMARY KEY (user_sub, jid)
);

CREATE INDEX IF NOT EXISTS idx_onboarding_token ON onboarding(token);

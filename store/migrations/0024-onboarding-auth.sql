-- The user_jids table. The three onboarding columns this file also added are
-- attributes of an objstore item now — token and token_expires went inert with
-- the 5/31 fold and onbod 0006 dropped them, and user_sub is written by
-- store.QueueOnboarding and its siblings.

CREATE TABLE IF NOT EXISTS user_jids (
    user_sub TEXT NOT NULL,
    jid      TEXT NOT NULL UNIQUE,
    claimed  TEXT NOT NULL,
    PRIMARY KEY (user_sub, jid)
);

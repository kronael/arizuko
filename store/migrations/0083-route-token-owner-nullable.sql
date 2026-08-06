-- Schema parity with routd migration 0032 so store's route_tokens read/write
-- paths (webd, proxyd, CLI, tests) see the same shape: owner_folder is NULLable
-- because a pairing token minted by onbod's greeting has no folder to reference
-- yet (spec 5/31 § owner_folder must go nullable). Rationale, rebuild pattern
-- and column order all live in the routd twin.

CREATE TABLE route_tokens_new (
    token_hash    BLOB PRIMARY KEY,
    jid           TEXT NOT NULL,
    owner_folder  TEXT REFERENCES groups(folder) ON DELETE CASCADE,
    created_at    TEXT NOT NULL,
    context       TEXT,
    kind          TEXT NOT NULL DEFAULT 'route'
);

INSERT INTO route_tokens_new (token_hash, jid, owner_folder, created_at, context, kind)
SELECT token_hash, jid, owner_folder, created_at, context, kind FROM route_tokens;

DROP TABLE route_tokens;
ALTER TABLE route_tokens_new RENAME TO route_tokens;

CREATE INDEX route_tokens_jid ON route_tokens(jid);

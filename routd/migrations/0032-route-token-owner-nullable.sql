-- route_tokens.owner_folder becomes NULLable (spec 5/31 § owner_folder must go
-- nullable). A DELIVERY token always has a real owning folder — that is why
-- 0069/its routd twin declared the column NOT NULL. A PAIRING token minted by
-- onbod's greeting is sent before any human is known and therefore before any
-- folder exists to reference; that absence is the whole reason onboarding is a
-- separate act from pairing. NULL is the truth. The alternative — a permanent
-- sentinel row in `groups` — is platform-manufactured operator data, which root
-- CLAUDE.md rules out.
--
-- NULL costs nothing functionally: neither PeekPairing nor RedeemPairing reads
-- owner_folder, and pair-kind rows are already excluded from ListRouteTokens and
-- revokeRouteTokenTx (both filtered to kind='route'). SQLite's FK check is a
-- no-op on NULL, so the constraint is satisfied by having nothing to reference.
--
-- Same rebuild pattern as 0069 (SQLite cannot drop NOT NULL in place). Column
-- order and the jid index are preserved verbatim; every existing row keeps its
-- owner_folder, so this migration changes what is WRITABLE, not what is stored.

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

-- 0069 — Declared FK: route_tokens.owner_folder → groups.folder ON DELETE CASCADE.
--
-- Spec 5/36 "FK posture": route_tokens.owner_folder is row-shaped
-- (InsertRouteToken rejects empty owner_folder, route_tokens.go:70) and
-- group removal MUST invalidate webhook tokens minted by that group —
-- silently dropping them is correct; the URL would 404 anyway because
-- the routing target is gone.
--
-- Same rebuild pattern as 0068. Re-create the route_tokens_jid index.
-- Orphan filter: tokens whose owner_folder has no groups row are dropped
-- (their target chain is broken already).

CREATE TABLE route_tokens_new (
    token_hash    BLOB PRIMARY KEY,
    jid           TEXT NOT NULL,
    owner_folder  TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
    created_at    TEXT NOT NULL
);

INSERT INTO route_tokens_new (token_hash, jid, owner_folder, created_at)
SELECT t.token_hash, t.jid, t.owner_folder, t.created_at
FROM route_tokens t
INNER JOIN groups g ON g.folder = t.owner_folder;

DROP TABLE route_tokens;
ALTER TABLE route_tokens_new RENAME TO route_tokens;

CREATE INDEX route_tokens_jid ON route_tokens(jid);

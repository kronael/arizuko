-- 0068 — Declared FK: web_routes.folder → groups.folder ON DELETE CASCADE.
--
-- Spec 5/36 "FK posture": web_routes is row-shaped (folder column always
-- non-empty, single writer in store/web_routes.go) and group removal MUST
-- invalidate URL routes that pin to the dead group. CASCADE delivers that
-- inside the apply tx without per-caller cleanup.
--
-- SQLite cannot ALTER TABLE ... ADD CONSTRAINT FOREIGN KEY. Rebuild the
-- table via CREATE _new + INSERT SELECT + DROP + RENAME (the pattern used
-- in 0020, 0051, 0054).
--
-- Orphan filter: rows whose folder doesn't exist in groups are dropped
-- (INNER JOIN). Such rows are already broken — they pin a URL to a group
-- that doesn't exist. Logged for posterity via the audit_log emit at the
-- end of this migration would be ideal but the migration runner doesn't
-- have that surface; a count-row warning lives in the migration log only.

CREATE TABLE web_routes_new (
    path_prefix TEXT PRIMARY KEY,
    access      TEXT NOT NULL CHECK(access IN ('public','auth','deny','redirect')),
    redirect_to TEXT,
    folder      TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
    created_at  TEXT NOT NULL
);

INSERT INTO web_routes_new (path_prefix, access, redirect_to, folder, created_at)
SELECT w.path_prefix, w.access, w.redirect_to, w.folder, w.created_at
FROM web_routes w
INNER JOIN groups g ON g.folder = w.folder;

DROP TABLE web_routes;
ALTER TABLE web_routes_new RENAME TO web_routes;

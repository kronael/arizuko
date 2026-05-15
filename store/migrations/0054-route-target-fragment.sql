-- v0.39.0: route mode rides on target as URI fragment. Drop impulse_config.
-- Spec: specs/6/B-route-mode-ingestion.md.
--
-- Conversion rules (one-time, no legacy fallback):
--   impulse_config NULL or no weights<100      -> target unchanged (trigger)
--   weights all zero / all listed verbs zero   -> target = target || '#observe'
--   weights mixed (some 0, some non-0)         -> target = target || '#observe'
--                                                 + INSERT one duplicate trigger
--                                                 row per non-zero verb with
--                                                 seq=seq-1 and match += verb=<v>
--
-- After conversion the impulse_config column is dropped.

-- Step 1: spawn higher-priority trigger rows for non-zero verbs in mixed
-- impulse configs. Use json_each over the weights map.
INSERT INTO routes (seq, match, target, impulse_config)
SELECT
  r.seq - 1,
  CASE WHEN r.match = '' THEN 'verb=' || je.key
       ELSE r.match || ' verb=' || je.key END,
  r.target,
  NULL
FROM routes r, json_each(json_extract(r.impulse_config, '$.weights')) je
WHERE r.impulse_config IS NOT NULL
  AND r.impulse_config != ''
  AND CAST(je.value AS INTEGER) != 0
  AND EXISTS (
    SELECT 1
    FROM json_each(json_extract(r.impulse_config, '$.weights'))
    WHERE CAST(value AS INTEGER) = 0
  );

-- Step 2: convert rows with any explicit weight=0 (i.e. configs that
-- suppress at least one verb) to observe-mode. Includes both the
-- "all zero" and "mixed" cases. The non-zero verbs are already covered
-- by the trigger duplicates inserted in step 1.
UPDATE routes
SET target = target || '#observe'
WHERE impulse_config IS NOT NULL
  AND impulse_config != ''
  AND EXISTS (
    SELECT 1
    FROM json_each(json_extract(impulse_config, '$.weights'))
    WHERE CAST(value AS INTEGER) = 0
  );

-- Step 3: drop impulse_config column. SQLite needs a table rebuild.
CREATE TABLE routes_new (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  seq                      INTEGER NOT NULL DEFAULT 0,
  match                    TEXT    NOT NULL DEFAULT '',
  target                   TEXT    NOT NULL,
  observe_window_messages  INTEGER,
  observe_window_chars     INTEGER
);

INSERT INTO routes_new (id, seq, match, target)
SELECT id, seq, match, target FROM routes;

DROP TABLE routes;
ALTER TABLE routes_new RENAME TO routes;
CREATE INDEX idx_routes_seq ON routes(seq);

-- Step 4: messages.is_observed marks inbound messages stored under a
-- folder via an observe-mode route. Surfacing query uses this flag.
ALTER TABLE messages ADD COLUMN is_observed INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_messages_observed ON messages(routed_to, is_observed, timestamp);

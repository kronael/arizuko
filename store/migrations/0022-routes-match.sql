-- Rebuild routes table with (id, seq, match, target, impulse_config) shape.
-- Drops: jid, type columns. Match is now a space-separated list of
-- key=glob pairs evaluated in Go against the incoming message.

CREATE TABLE routes_new (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  seq            INTEGER NOT NULL DEFAULT 0,
  match          TEXT    NOT NULL DEFAULT '',
  target         TEXT    NOT NULL,
  impulse_config TEXT
);

-- Translate existing rows:
--   type=default, empty match:       'room=<post-colon of jid>'
--   type=verb, match=<v>:             'room=<post-colon of jid> verb=<v>'
-- Everything else (prefix/command/keyword/pattern/sender) is dropped —
-- prefix and command are handled in gateway code now, and
-- keyword/pattern/sender were dead vocabulary in prod.
INSERT INTO routes_new (seq, match, target, impulse_config)
SELECT
  seq,
  CASE
    WHEN type = 'default' THEN
      CASE
        WHEN instr(jid, ':') > 0
          THEN 'room=' || substr(jid, instr(jid, ':') + 1)
        ELSE 'room=' || jid
      END
    WHEN type = 'verb' THEN
      CASE
        WHEN instr(jid, ':') > 0
          THEN 'room=' || substr(jid, instr(jid, ':') + 1) || ' verb=' || COALESCE(match, '')
        ELSE 'room=' || jid || ' verb=' || COALESCE(match, '')
      END
    ELSE NULL
  END,
  target,
  impulse_config
FROM routes
WHERE type IN ('default', 'verb');

DROP INDEX IF EXISTS idx_routes_jid_seq;
DROP TABLE routes;
ALTER TABLE routes_new RENAME TO routes;
CREATE INDEX idx_routes_seq ON routes(seq);

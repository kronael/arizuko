-- messages.db twin of routd/migrations/0014: collapse the per-group
-- behavioral columns (model, observe_window_messages, observe_window_chars,
-- open) into one `config` JSON column. messages.db's groups table never had
-- thread_replies (that column lives only in routd.db), so it is absent here.
-- container_config stays its own column (spawn-specific, core.GroupConfig).
--
-- DROP COLUMN edits groups in place; only non-NULL source values become
-- config keys so json_extract yields NULL for an unset field and the Go
-- layer applies its default.

ALTER TABLE groups ADD COLUMN config TEXT;

UPDATE groups SET config = (
  SELECT json_group_object(k, v) FROM (
    SELECT 'model' AS k, model AS v WHERE model IS NOT NULL
    UNION ALL SELECT 'observe_window_messages', observe_window_messages
      WHERE observe_window_messages IS NOT NULL
    UNION ALL SELECT 'observe_window_chars', observe_window_chars
      WHERE observe_window_chars IS NOT NULL
    UNION ALL SELECT 'open', open WHERE open IS NOT NULL
  )
);

ALTER TABLE groups DROP COLUMN model;
ALTER TABLE groups DROP COLUMN observe_window_messages;
ALTER TABLE groups DROP COLUMN observe_window_chars;
ALTER TABLE groups DROP COLUMN open;

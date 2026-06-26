-- Collapse the per-group behavioral columns (model, thread_replies,
-- observe_window_messages, observe_window_chars, open) into a single
-- `config` JSON column. container_config stays its own column — it is
-- spawn-specific and maps to core.GroupConfig.
--
-- DROP COLUMN edits groups in place (no recreate/rename), so the
-- web_routes/route_tokens FKs that REFERENCE groups(folder) stay intact.
-- Only non-NULL source values become config keys; a NULL stays omitted so
-- json_extract yields NULL and the Go layer applies its default.

ALTER TABLE groups ADD COLUMN config TEXT;

UPDATE groups SET config = (
  SELECT json_group_object(k, v) FROM (
    SELECT 'model' AS k, model AS v WHERE model IS NOT NULL
    UNION ALL SELECT 'thread_replies', thread_replies
      WHERE thread_replies IS NOT NULL
    UNION ALL SELECT 'observe_window_messages', observe_window_messages
      WHERE observe_window_messages IS NOT NULL
    UNION ALL SELECT 'observe_window_chars', observe_window_chars
      WHERE observe_window_chars IS NOT NULL
    UNION ALL SELECT 'open', open WHERE open IS NOT NULL
  )
);

ALTER TABLE groups DROP COLUMN model;
ALTER TABLE groups DROP COLUMN thread_replies;
ALTER TABLE groups DROP COLUMN observe_window_messages;
ALTER TABLE groups DROP COLUMN observe_window_chars;
ALTER TABLE groups DROP COLUMN open;

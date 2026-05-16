-- specs/6/F: cross-folder ambient. Two new columns on groups:
--   open                       -- 1 = visible to open siblings as ambient source
--   observe_window_messages    -- per-group cap (NULL = use env default)
--   observe_window_chars       -- per-group cap (NULL = use env default)
--
-- Per-route caps (routes.observe_window_*) still win over per-group;
-- per-group wins over env defaults. NULL means "inherit". Defaults to
-- open=1 so existing groups keep behaving like single-folder ambients.

ALTER TABLE groups ADD COLUMN open INTEGER NOT NULL DEFAULT 1;
ALTER TABLE groups ADD COLUMN observe_window_messages INTEGER;
ALTER TABLE groups ADD COLUMN observe_window_chars    INTEGER;

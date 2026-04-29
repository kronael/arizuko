CREATE TABLE network_rules (
  folder      TEXT NOT NULL,
  target      TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (folder, target)
);

CREATE INDEX idx_network_rules_folder ON network_rules(folder);

INSERT INTO network_rules (folder, target, created_at, created_by) VALUES
  ('', 'anthropic.com',     CURRENT_TIMESTAMP, 'system'),
  ('', 'api.anthropic.com', CURRENT_TIMESTAMP, 'system');
